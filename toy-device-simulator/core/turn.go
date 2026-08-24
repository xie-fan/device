package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"toy-device-simulator/protocol"
)

func (d *DeviceInstance) Speak(pcm []byte) (turnID string, uuid uint32, err error) {
	turnID, uuid, _, err = d.SpeakPermit(pcm, nil)
	return turnID, uuid, err
}

// SpeakPermit occupy 成功后再 tryAcquire；seqBefore 在 occupy 成功之后取样。
// tryAcquire 为 nil 时不占 speak_permit（Phase 1 CLI）。失败不得留下占用槽。
func (d *DeviceInstance) SpeakPermit(pcm []byte, tryAcquire func() bool) (turnID string, uuid uint32, seqBefore int, err error) {
	copied := append([]byte(nil), pcm...)

	d.deviceMu.Lock()
	d.connMu.Lock()
	st := d.connState
	d.connMu.Unlock()
	if d.finalizeStarted {
		d.deviceMu.Unlock()
		return "", 0, 0, fmt.Errorf("正在收口，不可 speak")
	}
	if !Speakable(st, d.fault) {
		d.deviceMu.Unlock()
		return "", 0, 0, fmt.Errorf("当前连接状态不可 speak（conn=%v fault=%s）", st, d.fault)
	}
	if d.slot.Occupied() {
		d.deviceMu.Unlock()
		return "", 0, 0, ErrSlotOccupied
	}

	uuid = d.allocUUIDLocked()
	turnID = fmt.Sprintf("turn_%d", time.Now().UnixNano())
	var framesPath, upPath, downPath, turnPath string
	if d.phase2Recording {
		framesPath, upPath, downPath, turnPath, err = RecordingPathsPhase2(d.cfg.Recording.OutputDir, d.cfg.DeviceID, d.instanceID, turnID)
	} else {
		framesPath, upPath, downPath, turnPath, err = RecordingPaths(d.cfg.Recording.OutputDir, d.cfg.DeviceID, turnID)
	}
	if err != nil {
		d.deviceMu.Unlock()
		return "", 0, 0, err
	}
	if err := d.slot.Occupy(turnID, uuid, copied); err != nil {
		d.deviceMu.Unlock()
		return "", 0, 0, err
	}
	if tryAcquire != nil && !tryAcquire() {
		d.slot.Vacate()
		d.deviceMu.Unlock()
		return "", 0, 0, ErrSpeakPermit
	}
	seqBefore = d.events.Seq()
	done := make(chan Event, 1)
	tr := &turnRuntime{
		id:         turnID,
		uuid:       uuid,
		fault:      d.fault,
		seqBefore:  seqBefore,
		startedAt:  time.Now().UTC(),
		drained:    make(chan struct{}),
		framesPath: framesPath,
		upPath:     upPath,
		downPath:   downPath,
		turnPath:   turnPath,
		done:       done,
	}
	d.turn = tr
	d.completionCh = done
	d.turnDone[turnID] = done
	pcmCopy := append([]byte(nil), d.slot.PCM()...)
	saveUp := d.cfg.Recording.SaveUplinkAudio
	d.deviceMu.Unlock()

	if saveUp {
		// 解锁后只建空文件，不写整段 PCM；内容由 onWritten → recorder 追加实际发出的帧。
		_ = os.MkdirAll(filepath.Dir(upPath), 0o755)
		if f, err := os.OpenFile(upPath, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_ = f.Close()
		}
	}

	go d.uplinkTurn(turnID, uuid, pcmCopy)
	return turnID, uuid, seqBefore, nil
}

// testBeforeUplinkEnqueue 仅测试：frozen/Terminal 检查已通过、即将 enqueueData。
// 用 atomic.Value 存 func，避免测试赋值与 uplinkTurn 读取形成数据竞争。
type uplinkEnqueueHook func()

var testBeforeUplinkEnqueue atomic.Value

func setTestBeforeUplinkEnqueue(fn uplinkEnqueueHook) {
	testBeforeUplinkEnqueue.Store(fn)
}

func runTestBeforeUplinkEnqueue() {
	v := testBeforeUplinkEnqueue.Load()
	if v == nil {
		return
	}
	if fn, ok := v.(uplinkEnqueueHook); ok && fn != nil {
		fn()
	}
}

func (d *DeviceInstance) uplinkTurn(turnID string, uuid uint32, pcm []byte) {
	frames := BuildUplinkFrames(pcm, uuid, d.sampleRate, d.cfg.Audio.SliceMs, d.fault, d.cfg.Audio.MaxPayloadSize)

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
			// 已冻结则停止且不入队，避免本帧 out+1 悬空。
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
		runTestBeforeUplinkEnqueue()
		// 持 deviceMu 入队，避免解锁窗口内 VAD / 失败 JSON / CancelTurn 把 Stage=1 排到 Stage=2/3 之后。
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
		d.deviceMu.Unlock()
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

func (d *DeviceInstance) WaitTurn(turnID string, timeout time.Duration) (Event, error) {
	d.deviceMu.Lock()
	if ev, ok := d.turnTerm[turnID]; ok && ev.Type == "turn_terminal" {
		d.deviceMu.Unlock()
		return ev, nil
	}
	if d.lastTerminal.Type == "turn_terminal" && d.lastTerminal.TurnID == turnID {
		ev := d.lastTerminal
		d.deviceMu.Unlock()
		return ev, nil
	}
	ch := d.turnDone[turnID]
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
		if ev.TurnID != "" && ev.TurnID != turnID {
			return Event{}, fmt.Errorf("turn 终态不匹配")
		}
		return ev, nil
	case <-t.C:
		return Event{}, ErrWaitTimeout
	case <-d.finalizeDone:
		d.deviceMu.Lock()
		defer d.deviceMu.Unlock()
		if ev, ok := d.turnTerm[turnID]; ok && ev.Type == "turn_terminal" {
			return ev, nil
		}
		if d.lastTerminal.Type == "turn_terminal" && d.lastTerminal.TurnID == turnID {
			return d.lastTerminal, nil
		}
		select {
		case ev := <-ch:
			if ev.TurnID == "" || ev.TurnID == turnID {
				return ev, nil
			}
		default:
		}
		return Event{}, errString("连接已收口且无 turn_terminal")
	}
}

func (d *DeviceInstance) WaitBudgetFor(pcmBytes int) time.Duration {
	cfg := d.Config()
	upload := UploadDuration(pcmBytes, cfg.Audio.SampleRate, cfg.Audio.Channels, 2, cfg.Audio.SliceMs)
	return WaitBudget(
		upload,
		seconds(cfg.Behavior.FirstReplyTimeoutSec),
		seconds(cfg.Behavior.DownlinkIdleTimeoutSec),
		seconds(cfg.Behavior.NonAudioFollowupSec),
		seconds(cfg.Behavior.PostFinalASRSilenceSec),
		seconds(cfg.Behavior.WaitTimeoutSlackSec),
	)
}
