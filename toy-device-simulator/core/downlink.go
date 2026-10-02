package core

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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
	case protocol.FirstImage:
		d.handleImageDownlink(raw)
	case protocol.FirstTrans:
		d.handleTransDownlink(raw)
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

	photoTTS := d.turn != nil && d.turn.waitingPhoto && h.UUID == 0
	matched := d.slot.Occupied() && (h.UUID == d.slot.UUID() || photoTTS)
	if matched && h.Stage == protocol.StageUploading {
		_, n := d.appendChunkLocked("tts_chunk", d.slot.ID(), len(view.Payload))
		acc = append(acc, n)
		if d.turn != nil {
			d.turn.hasTTS = true
			// Phase 5e：按首帧头记录下行实际格式（不再假定 PCM）。
			if d.turn.downFormat == "" {
				d.turn.downFormat = strings.TrimRight(string(h.AudioFormat[:]), "\x00")
				d.turn.downSampleRate = int(h.SamplingRate)
			}
			d.turn.downBytes += len(view.Payload)
			d.recorder.SubmitPCM(d.turn.downPath, view.Payload, false)
		}
		if h.NeedAck == 1 {
			d.enqueueAckLocked(h.SequenceNumber, protocol.DownlinkTTS, h.UUID, "")
		}
		d.routeRelatedLocked(raw, &acc, &tn)
		return
	}

	if h.NeedAck == 1 {
		d.enqueueAckLocked(h.SequenceNumber, protocol.DownlinkHintAudio, h.UUID, "")
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
	raw, err := protocol.EncodeAudioFrame(protocol.NewAudioHeader(d.cfg.Audio.Format, protocol.StageFinished, 0, d.slot.UUID(), 0, d.sampleRate), nil)
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
	var speak []chan SpeakableResult
	var speakCode int
	var speakState ConnState
	var photoJob *photoUploadJob
	d.deviceMu.Lock()
	defer func() {
		d.deviceMu.Unlock()
		notifySpeakable(speak, speakCode, speakState)
		d.finishCritical(acc, tn)
		if photoJob != nil {
			go d.runPhotoUpload(*photoJob)
		}
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
		speak, speakCode, speakState = d.maybeTakeSpeakableLocked()
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
		speak, speakCode, speakState = d.maybeTakeSpeakableLocked()
	case topicEnds(env.Topic, "/command/client"):
		cmd, _ := protocol.DecodeCommandData(env.Data)
		turnID := ""
		related := false
		if d.slot.Occupied() {
			turnID = d.slot.ID()
			st := d.slot.State()
			related = st == TurnSpeaking || st == TurnFinishingUpload || st == TurnWaitingReply
		}
		_, n := d.appendEventLocked("command_received", turnID, summarizeCommand(cmd), "", "", "")
		acc = append(acc, n)
		if cmd.NeedAck == 1 {
			d.enqueueAckLocked(uint32(cmd.SequenceNumber), protocol.DownlinkCommand, 0, env.Topic)
		}
		photo, isPhoto := protocol.DecodePhotoCommand(env.Data)
		if isPhoto {
			photoJob = d.handlePhotoCommandLocked(photo, turnID, related, &acc)
		}
		if related && d.turn != nil {
			d.turn.hasCmd = true
			d.routeRelatedLocked(raw, &acc, &tn)
		}
	}
}

// handleImageDownlink 处理 '2' 下行：信封 JSON（upload_image_slice/client 的
// ImageAck）或 100 字节 ImageHeader 二进制帧。只记事件，不进 turn 完成矩阵。
func (d *DeviceInstance) handleImageDownlink(raw []byte) {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	defer func() {
		d.deviceMu.Unlock()
		d.finishCritical(acc, tn)
	}()
	turnID := ""
	if d.slot.Occupied() {
		turnID = d.slot.ID()
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw[1:], &env); err == nil && env.Topic != "" {
		ack, err := protocol.DecodeImageAck(env.Data)
		if err != nil {
			_, n := d.appendEventLocked("image_downlink", turnID, env.Topic, "", "", "")
			acc = append(acc, n)
			return
		}
		_, n := d.appendEventLocked("image_ack", turnID,
			fmt.Sprintf("code=%d missing=%d", ack.Code, len(ack.Data)), "", "", "")
		acc = append(acc, n)
		return
	}
	if h, err := protocol.DecodeImageHeader(raw[1:]); err == nil && h.Head == protocol.ImageHeadMagic {
		_, n := d.appendEventLocked("image_downlink", turnID,
			fmt.Sprintf("stage=%d seq=%d bytes=%d", h.Stage, h.SequenceNumber, len(raw)-1-protocol.ImageHeaderBytes), "", "", "")
		acc = append(acc, n)
		return
	}
	_, n := d.appendEventLocked("protocol_error", turnID, "图片下行无法解码", "", "", "")
	acc = append(acc, n)
}

// handleTransDownlink 处理 '3' 下行：{topic:"…/trans/client", data:TransferData}，
// Response 由服务端回填。只记事件，不进 turn 完成矩阵。
func (d *DeviceInstance) handleTransDownlink(raw []byte) {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	defer func() {
		d.deviceMu.Unlock()
		d.finishCritical(acc, tn)
	}()
	turnID := ""
	if d.slot.Occupied() {
		turnID = d.slot.ID()
	}
	_, data, err := protocol.DecodeTransDownlink(raw)
	if err != nil {
		_, n := d.appendEventLocked("protocol_error", turnID, "转发下行无法解码", "", "", "")
		acc = append(acc, n)
		return
	}
	_, n := d.appendEventLocked("trans_response", turnID,
		fmt.Sprintf("path=%s status=%d", data.Request.Path, data.Response.StatusCode), "", "", "")
	acc = append(acc, n)
}

// summarizeCommand 把下行指令压成一行摘要写进 command_received 的 reason，
// 只列下发过的（非零）字段，不做行为响应。
func summarizeCommand(c protocol.CommandData) string {
	var b []string
	if c.SequenceNumber != 0 {
		b = append(b, fmt.Sprintf("seq=%d", c.SequenceNumber))
	}
	if c.Code != 0 {
		b = append(b, fmt.Sprintf("code=%d", c.Code))
	}
	if c.Message != "" {
		b = append(b, fmt.Sprintf("msg=%q", c.Message))
	}
	if c.SetVolume != 0 {
		b = append(b, fmt.Sprintf("setVolume=%d", c.SetVolume))
	}
	if c.SetTimbre != "" {
		b = append(b, fmt.Sprintf("setTimbre=%s", c.SetTimbre))
	}
	if c.ShutDown {
		b = append(b, "shutDown")
	}
	if c.PlayingMode != 0 {
		b = append(b, fmt.Sprintf("playingMode=%d", c.PlayingMode))
	}
	if c.Total != 0 {
		b = append(b, fmt.Sprintf("total=%d", c.Total))
	}
	if c.Light != 0 {
		b = append(b, fmt.Sprintf("light=%d", c.Light))
	}
	if c.Fan != 0 {
		b = append(b, fmt.Sprintf("fan=%d", c.Fan))
	}
	if len(c.Movements) > 0 {
		ids := make([]string, 0, len(c.Movements))
		for _, m := range c.Movements {
			ids = append(ids, strconv.Itoa(m.Behavior))
		}
		b = append(b, fmt.Sprintf("movements=%d[%s]", len(c.Movements), strings.Join(ids, ",")))
	}
	if c.Movement.Behavior != 0 {
		b = append(b, fmt.Sprintf("movement=%d", c.Movement.Behavior))
	}
	if len(c.Data) > 0 {
		b = append(b, "data")
	}
	if c.NeedAck != 0 {
		b = append(b, "need_ack")
	}
	return strings.Join(b, " ")
}

type photoUploadJob struct {
	turnID      string
	questionKey string
	imageID     string
	replyFormat string
	interval    time.Duration

	uuid    uint32
	tr      *turnRuntime
	related bool // 指令落在活跃轮里：传出的图才留档（同 photo_start_voice）
}

func (d *DeviceInstance) handlePhotoCommandLocked(photo protocol.PhotoCommand, turnID string, related bool, acc *[]EventNotify) *photoUploadJob {
	_, n := d.appendEventLocked("photo_command", turnID, photo.QuestionKey, "", "", "")
	*acc = append(*acc, n)
	if related && d.turn != nil && photo.StartVoice != "" {
		if raw, err := base64.StdEncoding.DecodeString(photo.StartVoice); err == nil && len(raw) > 0 {
			path := filepath.Join(filepath.Dir(d.turn.downPath), "photo_start_voice."+d.cfg.Audio.Format)
			d.recorder.SubmitPCM(path, raw, false)
		}
	}
	feat := d.cfg.Features.Photo
	skip := ""
	switch {
	case !feat.Enabled:
		skip = "功能没开"
	case feat.Image == "":
		skip = "没配图"
	case d.photoImage == nil:
		skip = "读图失败"
	}
	if skip != "" {
		_, n := d.appendEventLocked("photo_skipped", turnID, skip, "", "", "")
		*acc = append(*acc, n)
		return nil
	}
	if related && d.turn != nil {
		d.turn.waitingPhoto = true
		stopTimer(d.turn.followup)
		d.turn.followup = nil
	}
	reply := ""
	if !feat.ServerDefaultReply {
		reply = d.cfg.Audio.Format
	}
	return &photoUploadJob{
		turnID:      turnID,
		questionKey: photo.QuestionKey,
		imageID:     feat.Image,
		replyFormat: reply,
		interval:    time.Duration(cmp.Or(feat.SliceIntervalMs, 50)) * time.Millisecond,
		uuid:        d.allocUUIDLocked(),
		tr:          d.turn,
		related:     related,
	}
}

func (d *DeviceInstance) runPhotoUpload(job photoUploadJob) {
	data, format, err := d.photoImage(job.imageID)
	if err != nil || len(data) == 0 {
		d.notePhotoSkipped(job.turnID, "读图失败")
		return
	}
	frames, err := protocol.BuildImageFrames(data, format, job.questionKey, job.replyFormat, job.uuid)
	if err != nil {
		d.notePhotoSkipped(job.turnID, "读图失败")
		return
	}
	for i, raw := range frames {
		if i > 0 {
			time.Sleep(job.interval)
		}
		d.enqueueOrFinalize(Frame{Kind: KindManage, Raw: raw, turn: job.tr})
	}
	if job.related {
		d.savePhoto(job.tr, job.uuid, format, data)
	}
	d.deviceMu.Lock()
	_, n := d.appendEventLocked("photo_uploaded", job.turnID, photoReason("command", job.imageID, job.uuid, len(data), len(frames)), "", "", "")
	d.deviceMu.Unlock()
	d.finishCritical([]EventNotify{n}, TerminalNotify{})
}

func (d *DeviceInstance) notePhotoSkipped(turnID, reason string) {
	d.deviceMu.Lock()
	_, n := d.appendEventLocked("photo_skipped", turnID, reason, "", "", "")
	d.deviceMu.Unlock()
	d.finishCritical([]EventNotify{n}, TerminalNotify{})
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
	acc = append(acc, eventNotifyOf(tn))
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
		uuidOK := view.OKHeader && view.Header.Stage == protocol.StageUploading &&
			(view.Header.UUID == d.slot.UUID() || (d.turn.waitingPhoto && view.Header.UUID == 0))
		if uuidOK {
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
		if d.turn.waitingPhoto {
			d.armPhotoReplyLocked()
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

func jsonDownlinkType(t uint32) string {
	switch t {
	case protocol.DownlinkTTS:
		return "tts"
	case protocol.DownlinkHintAudio:
		return "hint_audio"
	case protocol.DownlinkCommand:
		return "command"
	default:
		return "tts"
	}
}

func (d *DeviceInstance) enqueueAckLocked(seq, downlinkType, uuid uint32, cmdTopic string) {
	code := uint32(d.cfg.Behavior.DownlinkAck.Code)
	sleepMs := d.cfg.Behavior.DownlinkAck.SleepMs
	d.throttle.Observe(sleepMs)
	var raw []byte
	var err error
	if d.cfg.Behavior.DownlinkAck.Mode == "json" {
		ack := protocol.JSONAck{
			Ack:            seq,
			SequenceNumber: seq,
			DownlinkType:   jsonDownlinkType(downlinkType),
			Code:           code,
			SleepMs:        uint32(sleepMs),
		}
		if downlinkType == protocol.DownlinkCommand {
			ack.Topic = cmdTopic
		} else {
			ack.UUID = uuid
		}
		raw, err = protocol.EncodeJSONAckFrame(d.cfg.Enterprise, d.cfg.DeviceType, d.cfg.DeviceID, ack)
	} else {
		m := protocol.AudioBinaryAck(seq, downlinkType, code)
		m.SleepMs = uint32(sleepMs)
		raw, err = protocol.EncodeAckFrame(m)
	}
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
	acc = append(acc, eventNotifyOf(tn))
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
		dur = 2 * time.Second
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

// armPhotoReplyLocked 停掉 followup，改用拍照回复超时（0 → 60s）。到期走同一条 onFollowup。
func (d *DeviceInstance) armPhotoReplyLocked() {
	if d.turn == nil {
		return
	}
	stopTimer(d.turn.followup)
	dur := time.Duration(cmp.Or(d.cfg.Features.Photo.ReplyTimeoutSec, 60)) * time.Second
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
