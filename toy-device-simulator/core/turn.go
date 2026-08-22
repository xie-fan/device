package core

import (
	"fmt"
	"time"

	"toy-device-simulator/protocol"
)

func (d *DeviceInstance) Speak(pcm []byte) (turnID string, uuid uint32, err error) {
	copied := append([]byte(nil), pcm...)

	d.deviceMu.Lock()
	d.connMu.Lock()
	st := d.connState
	d.connMu.Unlock()
	if d.finalizeStarted {
		d.deviceMu.Unlock()
		return "", 0, fmt.Errorf("正在收口，不可 speak")
	}
	if !Speakable(st, d.fault) {
		d.deviceMu.Unlock()
		return "", 0, fmt.Errorf("当前连接状态不可 speak（conn=%v fault=%s）", st, d.fault)
	}
	if d.slot.Occupied() {
		d.deviceMu.Unlock()
		return "", 0, fmt.Errorf("槽已占用")
	}

	uuid = d.allocUUIDLocked()
	turnID = fmt.Sprintf("turn_%d", time.Now().UnixNano())
	if err := d.slot.Occupy(turnID, uuid, copied); err != nil {
		d.deviceMu.Unlock()
		return "", 0, err
	}
	framesPath, upPath, downPath, turnPath := RecordingPaths(d.cfg.Recording.OutputDir, d.cfg.DeviceID, turnID)
	tr := &turnRuntime{
		id:         turnID,
		uuid:       uuid,
		fault:      d.fault,
		seqBefore:  d.events.Seq(),
		startedAt:  time.Now().UTC(),
		drained:    make(chan struct{}),
		framesPath: framesPath,
		upPath:     upPath,
		downPath:   downPath,
		turnPath:   turnPath,
	}
	d.turn = tr
	d.completionCh = make(chan Event, 1)
	pcmCopy := append([]byte(nil), d.slot.PCM()...)
	d.deviceMu.Unlock()

	go d.uplinkTurn(turnID, uuid, pcmCopy)
	return turnID, uuid, nil
}

func (d *DeviceInstance) uplinkTurn(turnID string, uuid uint32, pcm []byte) {
	frames := BuildUplinkFrames(pcm, uuid, d.sampleRate, d.cfg.Audio.SliceMs, d.fault)

	d.deviceMu.Lock()
	if d.slot.ID() != turnID || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	d.slot.SetState(TurnSpeaking)
	d.deviceMu.Unlock()

	pace := time.Duration(d.cfg.Audio.SliceMs) * time.Millisecond
	for _, raw := range frames {
		d.deviceMu.Lock()
		if d.finalizeStarted || d.slot.ID() != turnID || d.slot.State() == TurnTerminal {
			d.deviceMu.Unlock()
			return
		}
		if d.turn != nil && d.turn.frozen {
			d.deviceMu.Unlock()
			break
		}
		view := protocol.Inspect(raw)
		stage := view.Header.Stage
		seq := view.Header.SequenceNumber
		if view.First == protocol.FirstAudio && !view.OKHeader {
			stage = 1
		}
		if stage == protocol.StageFinished {
			d.slot.SetState(TurnFinishingUpload)
		}
		injected := ""
		if d.turn != nil {
			injected = string(d.turn.fault)
		}
		d.turn.out.Add(1)
		tr := d.turn
		d.deviceMu.Unlock()

		err := d.enqueueData(Frame{
			Kind:          KindAudioData,
			TurnID:        turnID,
			UUID:          uuid,
			Stage:         stage,
			Seq:           seq,
			Raw:           raw,
			InjectedFault: injected,
			turn:          tr,
		})
		if err != nil {
			d.deviceMu.Lock()
			if d.turn != nil {
				d.turn.out.Add(-1)
				if d.turn.out.Load() <= 0 {
					d.turn.signalDrained()
				}
			}
			d.deviceMu.Unlock()
			if err == ErrDataFull {
				d.requestFinalizeAsync("write_backpressure", false)
			}
			return
		}
		if stage == protocol.StageUploading && pace > 0 {
			select {
			case <-time.After(pace):
			case <-d.finalizeDone:
				return
			}
		}
	}

	d.waitUplinkDrained(turnID)

	d.deviceMu.Lock()
	if d.slot.ID() == turnID && d.slot.State() != TurnTerminal && !d.finalizeStarted {
		d.enterWaitingReplyLocked()
	}
	d.deviceMu.Unlock()
}

func (d *DeviceInstance) waitUplinkDrained(turnID string) {
	d.deviceMu.Lock()
	tr := d.turn
	d.deviceMu.Unlock()
	if tr == nil || tr.id != turnID {
		return
	}
	if tr.out.Load() <= 0 {
		tr.signalDrained()
		return
	}
	select {
	case <-tr.drained:
	case <-d.finalizeDone:
	case <-time.After(d.drainTimeout + 5*time.Second):
	}
}

func (d *DeviceInstance) enterWaitingReplyLocked() {
	if d.slot.State() == TurnTerminal || d.finalizeStarted {
		return
	}
	d.slot.SetState(TurnWaitingReply)
	replay := [][]byte(nil)
	if d.turn != nil {
		replay = d.turn.early.Drain()
	}
	d.armFirstReplyLocked()
	for _, raw := range replay {
		d.replayDownlinkLocked(raw)
	}
}

func (d *DeviceInstance) WaitTurn(timeout time.Duration) (Event, error) {
	d.deviceMu.Lock()
	ch := d.completionCh
	if d.lastTerminal.Type == "turn_terminal" {
		ev := d.lastTerminal
		d.deviceMu.Unlock()
		return ev, nil
	}
	d.deviceMu.Unlock()
	if ch == nil {
		return Event{}, fmt.Errorf("没有等待中的 Turn")
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case ev := <-ch:
		return ev, nil
	case <-t.C:
		return Event{}, ErrWaitTimeout
	case <-d.finalizeDone:
		d.deviceMu.Lock()
		defer d.deviceMu.Unlock()
		if d.lastTerminal.Type == "turn_terminal" {
			return d.lastTerminal, nil
		}
		return Event{}, errString("连接已收口且无 turn_terminal")
	}
}

func (d *DeviceInstance) WaitBudgetFor(pcmBytes int) time.Duration {
	upload := UploadDuration(pcmBytes, d.cfg.Audio.SampleRate, d.cfg.Audio.Channels, 2, d.cfg.Audio.SliceMs)
	return WaitBudget(
		upload,
		seconds(d.cfg.Behavior.FirstReplyTimeoutSec),
		seconds(d.cfg.Behavior.DownlinkIdleTimeoutSec),
		seconds(d.cfg.Behavior.NonAudioFollowupSec),
		seconds(d.cfg.Behavior.PostFinalASRSilenceSec),
		seconds(d.cfg.Behavior.WaitTimeoutSlackSec),
	)
}
