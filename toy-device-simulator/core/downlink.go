package core

import (
	"fmt"
	"strconv"
	"time"

	"toy-device-simulator/protocol"
	"toy-device-simulator/recording"
)

func (d *DeviceInstance) handleInbound(raw []byte) {
	if len(raw) == 0 {
		d.deviceMu.Lock()
		_, n := d.appendEventLocked("protocol_error", "", "空帧", "", "", "")
		d.deviceMu.Unlock()
		d.finishCritical([]EventNotify{n}, TerminalNotify{})
		return
	}

	now := time.Now()
	d.deviceMu.Lock()
	if d.turn != nil {
		d.recorder.SubmitFrame(d.turn.framesPath, recording.RowFromRaw("inbound", raw, "", now))
	}
	d.deviceMu.Unlock()

	switch raw[0] {
	case protocol.FirstAudio:
		d.handleAudioDownlink(raw)
	case protocol.FirstManage:
		d.handleManageDownlink(raw)
	case protocol.FirstJSON:
		d.handleUnprefixedJSON(raw)
	case protocol.FirstAck:
		return
	default:
		d.deviceMu.Lock()
		turnID := ""
		if d.slot.Occupied() {
			turnID = d.slot.ID()
		}
		_, n := d.appendEventLocked("protocol_error", turnID, "非法首字节", "", "", "")
		d.deviceMu.Unlock()
		d.finishCritical([]EventNotify{n}, TerminalNotify{})
	}
}

func (d *DeviceInstance) handleAudioDownlink(raw []byte) {
	view := protocol.Inspect(raw)
	var acc []EventNotify
	var tn TerminalNotify

	d.deviceMu.Lock()
	defer func() {
		d.deviceMu.Unlock()
		d.finishCritical(acc, tn)
	}()

	if !view.OKHeader {
		turnID := ""
		if d.slot.Occupied() {
			turnID = d.slot.ID()
		}
		_, n := d.appendEventLocked("protocol_error", turnID, "音频头不足 100 字节", "", "", "")
		acc = append(acc, n)
		return
	}

	h := view.Header
	if h.Stage == protocol.StageVAD {
		d.handleVADLocked(h, &acc)
		return
	}

	matched := d.slot.Occupied() && h.UUID == d.slot.UUID()
	if matched && h.Stage == protocol.StageUploading {
		_, n := d.appendEventLocked("tts_chunk", d.slot.ID(), "", "", "", "")
		acc = append(acc, n)
		if d.turn != nil {
			d.turn.hasTTS = true
			d.recorder.SubmitPCM(d.turn.downPath, view.Payload, false)
		}
		if h.NeedAck == 1 {
			d.enqueueAckLocked(h.SequenceNumber, protocol.DownlinkTTS)
		}
		d.routeRelatedLocked(raw, &acc, &tn)
		return
	}

	if h.NeedAck == 1 {
		d.enqueueAckLocked(h.SequenceNumber, protocol.DownlinkHintAudio)
	}
}

func (d *DeviceInstance) handleVADLocked(h protocol.AudioHeader, acc *[]EventNotify) {
	if !d.slot.Occupied() || h.UUID != d.slot.UUID() {
		return
	}
	if d.slot.UplinkEnd() == "" {
		d.slot.SetUplinkEnd("vad")
	}
	_, n := d.appendEventLocked("vad", d.slot.ID(), "", "", "", "")
	*acc = append(*acc, n)
	if d.turn != nil {
		d.turn.frozen = true
	}
	if d.slot.State() != TurnWaitingReply && d.slot.State() != TurnTerminal {
		d.enqueueStage2NowLocked()
	}
}

func (d *DeviceInstance) enqueueStage2NowLocked() {
	if d.turn == nil || d.finalizeStarted {
		return
	}
	raw, err := protocol.EncodeAudioFrame(protocol.NewPCMHeader(protocol.StageFinished, 0, d.slot.UUID(), 0, d.sampleRate), nil)
	if err != nil {
		return
	}
	d.slot.SetState(TurnFinishingUpload)
	d.turn.out.Add(1)
	if err := d.enqueueData(Frame{
		Kind:   KindAudioData,
		TurnID: d.slot.ID(),
		UUID:   d.slot.UUID(),
		Stage:  protocol.StageFinished,
		Raw:    raw,
		turn:   d.turn,
	}); err != nil {
		d.turn.out.Add(-1)
		if err == ErrDataFull {
			go d.requestFinalizeAsync("write_backpressure", false)
		}
	}
}

func (d *DeviceInstance) handleManageDownlink(raw []byte) {
	env, err := protocol.DecodeManage(raw)
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	defer func() {
		d.deviceMu.Unlock()
		d.finishCritical(acc, tn)
	}()
	if err != nil {
		_, n := d.appendEventLocked("protocol_error", "", "管理信封无法解码", "", "", "")
		acc = append(acc, n)
		return
	}

	switch {
	case topicEnds(env.Topic, "/register/client"):
		ack, err := protocol.DecodeRegisterAck(env.Data)
		if err != nil {
			_, n := d.appendEventLocked("protocol_error", "", "register ACK 无法解码", "", "", "")
			acc = append(acc, n)
			return
		}
		n, fin := d.handleRegisterAckLocked(ack.Code)
		acc = append(acc, n...)
		if fin {
			go d.requestFinalizeAsync("ack_failure", false)
		}
	case topicEnds(env.Topic, "/report/client"):
		rep, err := protocol.DecodeReportData(env.Data)
		if err != nil {
			_, n := d.appendEventLocked("protocol_error", "", "report 回显无法解码", "", "", "")
			acc = append(acc, n)
			return
		}
		n, _ := d.handleReportEchoLocked(rep.SequenceNumber)
		acc = append(acc, n...)
	case topicEnds(env.Topic, "/command/client"):
		cmd, _ := protocol.DecodeCommandData(env.Data)
		turnID := ""
		related := false
		if d.slot.Occupied() {
			turnID = d.slot.ID()
			st := d.slot.State()
			related = st == TurnSpeaking || st == TurnFinishingUpload || st == TurnWaitingReply
		}
		_, n := d.appendEventLocked("command_received", turnID, "", "", "", "")
		acc = append(acc, n)
		if cmd.NeedAck == 1 {
			d.enqueueAckLocked(uint32(cmd.SequenceNumber), protocol.DownlinkCommand)
		}
		if related && d.turn != nil {
			d.turn.hasCmd = true
			d.routeRelatedLocked(raw, &acc, &tn)
		}
	}
}

func (d *DeviceInstance) handleUnprefixedJSON(raw []byte) {
	u, err := protocol.DecodeUnprefixedJSON(raw)
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	defer func() {
		d.deviceMu.Unlock()
		d.finishCritical(acc, tn)
	}()
	if err != nil {
		_, n := d.appendEventLocked("protocol_error", "", "无前缀 JSON 无法解码", "", "", "")
		acc = append(acc, n)
		return
	}

	if protocol.IsFailedJSON(u.Code) {
		n, term := d.applyFailedJSONLocked(fmt.Sprintf("code=%d %s", u.Code, u.CodeMsg))
		acc = append(acc, n...)
		tn = term
		return
	}

	if u.Action == "asr_result" {
		turnID := ""
		if d.slot.Occupied() {
			turnID = d.slot.ID()
		}
		_, n := d.appendEventLocked("asr_result", turnID, "", "", "", "")
		acc = append(acc, n)
		if d.slot.Occupied() && d.turn != nil && u.SessionID == strconv.FormatUint(uint64(d.slot.UUID()), 10) {
			if u.IsFinal {
				d.turn.hasFinal = true
			} else {
				d.turn.hasInter = true
			}
		}
		return
	}

	if u.Code == 0 {
		turnID := ""
		related := false
		if d.slot.Occupied() {
			turnID = d.slot.ID()
			st := d.slot.State()
			related = st == TurnSpeaking || st == TurnFinishingUpload || st == TurnWaitingReply
		}
		_, n := d.appendEventLocked("json_reply", turnID, "", "", "", "")
		acc = append(acc, n)
		if related && d.turn != nil {
			d.turn.hasJSON = true
			d.routeRelatedLocked(raw, &acc, &tn)
		}
	}
}

func (d *DeviceInstance) applyFailedJSONLocked(reason string) ([]EventNotify, TerminalNotify) {
	var acc []EventNotify
	turnID := ""
	if d.slot.Occupied() {
		turnID = d.slot.ID()
	}
	_, n := d.appendEventLocked("protocol_error", turnID, reason, "", "", "")
	acc = append(acc, n)
	if !d.slot.Occupied() {
		return acc, TerminalNotify{}
	}
	if d.turn != nil {
		d.turn.frozen = true
	}
	cr := d.cancelTurnLocked()
	if cr == CancelBackpressure {
		_, n2 := d.appendEventLocked("local_validation_error", turnID, "stage3_backpressure", "", "", "")
		acc = append(acc, n2)
	}
	tn := d.terminalLocked(EndError, ReplyEmpty, "error", false)
	acc = append(acc, EventNotify{EventWaiters: tn.EventWaiters, SlowSubs: tn.SlowSubs})
	return acc, tn
}

func (d *DeviceInstance) cancelTurnLocked() CancelResult {
	if d.turn != nil {
		d.turn.frozen = true
	}
	send := CancelSendsStage3(d.slot.State())
	uuid := d.slot.UUID()
	turnID := d.slot.ID()
	d.connMu.Lock()
	d.writePumpMu.Lock()
	before := audioOpenCount(d.outbound, turnID)
	cr := d.outbound.CancelTurn(turnID, uuid, send)
	d.outbound.FillEmptyStage3(d.encodeStage3)
	tr := d.turn
	d.outbound.AttachMeta(func(f *Frame) {
		if f.Kind == KindStage3 && tr != nil {
			f.turn = tr
		}
	})
	after := audioOpenCount(d.outbound, turnID)
	deleted := before - after
	if d.turn != nil && deleted > 0 {
		if d.turn.out.Add(-int64(deleted)) <= 0 {
			d.turn.signalDrained()
		}
	}
	d.writePumpCond.Signal()
	d.writePumpMu.Unlock()
	d.connMu.Unlock()
	return cr
}

func (d *DeviceInstance) routeRelatedLocked(raw []byte, acc *[]EventNotify, tn *TerminalNotify) {
	if d.finalizeStarted || !d.slot.Occupied() || d.turn == nil {
		return
	}
	st := d.slot.State()
	if st != TurnWaitingReply {
		if overflow := d.turn.early.Push(raw); overflow {
			_, n := d.appendEventLocked("early_downlink_overflow", d.slot.ID(), "", "", "", "")
			*acc = append(*acc, n)
		}
		return
	}
	d.applyWaitingTimersLocked(raw)
	dec := DecideCompletion(d.completionInputLocked())
	if dec.Terminal {
		a, term := d.applyDecisionLocked(dec)
		*acc = append(*acc, a...)
		*tn = term
	}
}

func (d *DeviceInstance) replayDownlinkLocked(raw []byte) {
	if len(raw) == 0 || d.turn == nil {
		return
	}
	d.applyWaitingTimersLocked(raw)
}

func (d *DeviceInstance) applyWaitingTimersLocked(raw []byte) {
	if d.turn == nil || d.slot.State() != TurnWaitingReply {
		return
	}
	switch raw[0] {
	case protocol.FirstAudio:
		view := protocol.Inspect(raw)
		if view.OKHeader && view.Header.UUID == d.slot.UUID() && view.Header.Stage == protocol.StageUploading {
			d.armTTSIdleLocked()
			stopTimer(d.turn.firstReply)
			d.turn.firstReply = nil
			stopTimer(d.turn.followup)
			d.turn.followup = nil
		}
	case protocol.FirstManage:
		if d.turn.hasTTS {
			return
		}
		d.armFollowupLocked()
	case protocol.FirstJSON:
		if d.turn.hasTTS {
			return
		}
		d.armFollowupLocked()
	}
}

func (d *DeviceInstance) enqueueAckLocked(seq, downlinkType uint32) {
	code := uint32(d.cfg.Behavior.DownlinkAck.Code)
	raw, err := protocol.EncodeAckFrame(protocol.AudioBinaryAck(seq, downlinkType, code))
	if err != nil {
		return
	}
	d.enqueueOrFinalize(Frame{Kind: KindACK, Raw: raw, Seq: seq, turn: d.turn})
}

func (d *DeviceInstance) completionInputLocked() CompletionInput {
	phase := PhaseBeforeWaiting
	if d.finalizeStarted {
		phase = PhaseFinalizeStarted
	} else if d.slot.State() == TurnWaitingReply {
		phase = PhaseWaitingReply
	}
	in := CompletionInput{Phase: phase}
	if d.turn != nil {
		in.HasTTS = d.turn.hasTTS
		in.HasCommand = d.turn.hasCmd
		in.HasSuccessJSON = d.turn.hasJSON
		in.HasIsFinal = d.turn.hasFinal
		in.HasInterim = d.turn.hasInter
		in.FaultDropRow = FaultExpectsDrop(d.turn.fault)
	}
	return in
}

func (d *DeviceInstance) applyDecisionLocked(dec Decision) ([]EventNotify, TerminalNotify) {
	if !dec.Terminal {
		return nil, TerminalNotify{}
	}
	var acc []EventNotify
	turnID := d.slot.ID()
	if dec.ExtraEvent != "" {
		_, n := d.appendEventLocked(dec.ExtraEvent, turnID, "", "", "", "")
		acc = append(acc, n)
	}
	if dec.Pump == PumpCancelTurn {
		cr := d.cancelTurnLocked()
		if cr == CancelBackpressure {
			_, n := d.appendEventLocked("local_validation_error", turnID, "stage3_backpressure", "", "", "")
			acc = append(acc, n)
		}
	}
	uplinkIfEmpty := ""
	switch dec.EndReason {
	case EndError:
		uplinkIfEmpty = "error"
	case EndInterrupt:
		uplinkIfEmpty = "interrupt"
	}
	tn := d.terminalLocked(dec.EndReason, dec.ReplyKind, uplinkIfEmpty, false)
	acc = append(acc, EventNotify{EventWaiters: tn.EventWaiters, SlowSubs: tn.SlowSubs})
	return acc, tn
}

func (d *DeviceInstance) armFirstReplyLocked() {
	if d.turn == nil {
		return
	}
	stopTimer(d.turn.firstReply)
	dur := seconds(d.cfg.Behavior.FirstReplyTimeoutSec)
	if dur <= 0 {
		dur = 20 * time.Second
	}
	turnID := d.slot.ID()
	d.turn.firstReply = time.AfterFunc(dur, func() { d.onFirstReply(turnID) })
}

func (d *DeviceInstance) armTTSIdleLocked() {
	if d.turn == nil {
		return
	}
	stopTimer(d.turn.ttsIdle)
	dur := seconds(d.cfg.Behavior.DownlinkIdleTimeoutSec)
	if dur <= 0 {
		dur = 20 * time.Second
	}
	turnID := d.slot.ID()
	d.turn.ttsIdle = time.AfterFunc(dur, func() { d.onTTSIdle(turnID) })
}

func (d *DeviceInstance) armFollowupLocked() {
	if d.turn == nil || d.turn.followup != nil {
		return
	}
	dur := seconds(d.cfg.Behavior.NonAudioFollowupSec)
	if dur <= 0 {
		dur = 5 * time.Second
	}
	turnID := d.slot.ID()
	d.turn.followup = time.AfterFunc(dur, func() { d.onFollowup(turnID) })
}

func (d *DeviceInstance) armSilentLocked() {
	if d.turn == nil || d.turn.silent != nil {
		return
	}
	dur := seconds(d.cfg.Behavior.PostFinalASRSilenceSec)
	if dur <= 0 {
		dur = 5 * time.Second
	}
	turnID := d.slot.ID()
	d.turn.silent = time.AfterFunc(dur, func() { d.onSilent(turnID) })
}

func (d *DeviceInstance) onFirstReply(turnID string) {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	if d.turn == nil || d.turn.id != turnID || d.slot.State() != TurnWaitingReply || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	in := d.completionInputLocked()
	in.FirstReplyExpired = true
	if CanStartSilentTimer(true, in.HasIsFinal, in.HasTTS || in.HasCommand || in.HasSuccessJSON) {
		d.armSilentLocked()
		d.deviceMu.Unlock()
		return
	}
	dec := DecideCompletion(in)
	if dec.Terminal {
		acc, tn = d.applyDecisionLocked(dec)
	}
	d.deviceMu.Unlock()
	d.finishCritical(acc, tn)
}

func (d *DeviceInstance) onTTSIdle(turnID string) {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	if d.turn == nil || d.turn.id != turnID || d.slot.State() != TurnWaitingReply || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	in := d.completionInputLocked()
	in.TTSIdleExpired = true
	dec := DecideCompletion(in)
	if dec.Terminal {
		acc, tn = d.applyDecisionLocked(dec)
	}
	d.deviceMu.Unlock()
	d.finishCritical(acc, tn)
}

func (d *DeviceInstance) onFollowup(turnID string) {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	if d.turn == nil || d.turn.id != turnID || d.slot.State() != TurnWaitingReply || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	in := d.completionInputLocked()
	in.FollowupExpired = true
	dec := DecideCompletion(in)
	if dec.Terminal {
		acc, tn = d.applyDecisionLocked(dec)
	}
	d.deviceMu.Unlock()
	d.finishCritical(acc, tn)
}

func (d *DeviceInstance) onSilent(turnID string) {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	if d.turn == nil || d.turn.id != turnID || d.slot.State() != TurnWaitingReply || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	in := d.completionInputLocked()
	in.SilentExpired = true
	dec := DecideCompletion(in)
	if dec.Terminal {
		acc, tn = d.applyDecisionLocked(dec)
	}
	d.deviceMu.Unlock()
	d.finishCritical(acc, tn)
}
