package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"toy-device-simulator/protocol"
)

// SpeakResult：Queued=true 时只有 TurnID / QueuePos 有效（uuid、seq 在出队执行时才产生）。
type SpeakResult struct {
	TurnID    string
	UUID      uint32
	SeqBefore int
	Queued    bool
	QueuePos  int
}

type queuedSpeak struct {
	turnID     string
	pcm        []byte
	sp         *streamSpec // 非 nil = 压缩格式流式上行（Phase 5d）
	tryAcquire func() bool
}

func (d *DeviceInstance) Speak(pcm []byte) (turnID string, uuid uint32, err error) {
	res, err := d.SpeakPermit(pcm, nil)
	return res.TurnID, res.UUID, err
}

// SpeakPermit occupy 成功后再 tryAcquire；seqBefore 在 occupy 成功之后取样。
// tryAcquire 为 nil 时不占 speak_permit（Phase 1 CLI）。失败不得留下占用槽。
// Phase 4：槽占用且 speak_backlog_depth>0 时入队（Queued=true），终态后自动出队。
func (d *DeviceInstance) SpeakPermit(pcm []byte, tryAcquire func() bool) (SpeakResult, error) {
	return d.speakPermit(append([]byte(nil), pcm...), nil, tryAcquire)
}

// SpeakStreamPermit Phase 5d：压缩格式上行。open 的实时流（ffmpeg -re）按
// chunkBytes 聚合帧化发送；占槽/permit/backlog 语义与 SpeakPermit 一致。
func (d *DeviceInstance) SpeakStreamPermit(open StreamOpen, durMs, chunkBytes int, tryAcquire func() bool) (SpeakResult, error) {
	return d.speakPermit(nil, &streamSpec{open: open, durMs: durMs, chunk: chunkBytes}, tryAcquire)
}

func (d *DeviceInstance) speakPermit(copied []byte, sp *streamSpec, tryAcquire func() bool) (SpeakResult, error) {
	d.deviceMu.Lock()
	d.connMu.Lock()
	st := d.connState
	d.connMu.Unlock()
	if d.finalizeStarted {
		d.deviceMu.Unlock()
		return SpeakResult{}, fmt.Errorf("正在收口，不可 speak")
	}
	if !Speakable(st, d.fault) {
		d.deviceMu.Unlock()
		return SpeakResult{}, fmt.Errorf("当前连接状态不可 speak（conn=%v fault=%s）", st, d.fault)
	}
	if d.slot.Occupied() {
		depth := d.cfg.Behavior.SpeakBacklogDepth
		if depth <= 0 {
			d.deviceMu.Unlock()
			return SpeakResult{}, ErrSlotOccupied
		}
		if len(d.speakBacklog) >= depth {
			d.deviceMu.Unlock()
			return SpeakResult{}, ErrBacklogFull
		}
		turnID := d.allocTurnIDLocked()
		d.turnDone[turnID] = make(chan Event, 1)
		d.speakBacklog = append(d.speakBacklog, queuedSpeak{turnID: turnID, pcm: copied, sp: sp, tryAcquire: tryAcquire})
		pos := len(d.speakBacklog)
		_, en := d.appendEventLocked("speak_queued", turnID, fmt.Sprintf("pos=%d", pos), "", "", "")
		d.deviceMu.Unlock()
		en.NotifyHTTP()
		// 兜底：排队判定与 terminal 竞态时（dispatch 已跑完）自己再触发一次。
		go d.dispatchBacklog()
		return SpeakResult{TurnID: turnID, Queued: true, QueuePos: pos}, nil
	}

	turnID := d.allocTurnIDLocked()
	uuid, seqBefore, launch, err := d.startTurnLocked(turnID, copied, sp, tryAcquire)
	if err != nil {
		d.deviceMu.Unlock()
		return SpeakResult{}, err
	}
	d.deviceMu.Unlock()
	launch()
	return SpeakResult{TurnID: turnID, UUID: uuid, SeqBefore: seqBefore}, nil
}

// allocTurnIDLocked 纳秒时间戳 + 防撞（同纳秒连续分配时）。
func (d *DeviceInstance) allocTurnIDLocked() string {
	id := fmt.Sprintf("turn_%d", time.Now().UnixNano())
	for {
		if _, exists := d.turnDone[id]; !exists {
			return id
		}
		id += "x"
	}
}

// startTurnLocked 占槽并构造 turnRuntime；调用方必须持 deviceMu 且已确认槽空闲。
// 返回的 launch 必须在解锁后调用（建录音文件、起上行协程）。失败不留占用槽。
// sp 非 nil 时走流式上行（copied 应为 nil）。
func (d *DeviceInstance) startTurnLocked(turnID string, copied []byte, sp *streamSpec, tryAcquire func() bool) (uuid uint32, seqBefore int, launch func(), err error) {
	uuid = d.allocUUIDLocked()
	var framesPath, upPath, downPath, turnPath string
	if d.phase2Recording {
		framesPath, upPath, downPath, turnPath, err = RecordingPathsPhase2(d.cfg.Recording.OutputDir, d.cfg.DeviceID, d.instanceID, turnID)
	} else {
		framesPath, upPath, downPath, turnPath, err = RecordingPaths(d.cfg.Recording.OutputDir, d.cfg.DeviceID, turnID)
	}
	if err != nil {
		return 0, 0, nil, err
	}
	if sp != nil && copied == nil {
		// 流式上行无预置 PCM；空切片满足 Occupy 的「已拷贝」保护语义。
		copied = []byte{}
	}
	if err := d.slot.Occupy(turnID, uuid, copied); err != nil {
		return 0, 0, nil, err
	}
	if tryAcquire != nil && !tryAcquire() {
		d.slot.Vacate()
		return 0, 0, nil, ErrSpeakPermit
	}
	seqBefore = d.events.Seq()
	done := d.turnDone[turnID]
	if done == nil {
		done = make(chan Event, 1)
		d.turnDone[turnID] = done
	}
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
	pcmCopy := append([]byte(nil), d.slot.PCM()...)
	saveUp := d.cfg.Recording.SaveUplinkAudio
	launch = func() {
		if saveUp {
			// 解锁后只建空文件，不写整段 PCM；内容由 onWritten → recorder 追加实际发出的帧。
			_ = os.MkdirAll(filepath.Dir(upPath), 0o755)
			if f, err := os.OpenFile(upPath, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_ = f.Close()
			}
		}
		if sp != nil {
			go d.uplinkTurnStream(turnID, uuid, *sp)
			return
		}
		go d.uplinkTurn(turnID, uuid, pcmCopy)
	}
	return uuid, seqBefore, launch, nil
}

// dispatchBacklog 终态后（finishCritical）异步出队。队首启动失败则记
// speak_backlog_dropped 并继续尝试下一项；成功启动即返回，等下一次终态。
func (d *DeviceInstance) dispatchBacklog() {
	for {
		d.deviceMu.Lock()
		if d.finalizeStarted || len(d.speakBacklog) == 0 || d.slot.Occupied() {
			d.deviceMu.Unlock()
			return
		}
		q := d.speakBacklog[0]
		d.speakBacklog = d.speakBacklog[1:]
		d.connMu.Lock()
		st := d.connState
		d.connMu.Unlock()
		if !Speakable(st, d.fault) {
			en := d.dropQueuedLocked(q, "not_speakable")
			d.deviceMu.Unlock()
			en.NotifyHTTP()
			continue
		}
		uuid, seqBefore, launch, err := d.startTurnLocked(q.turnID, q.pcm, q.sp, q.tryAcquire)
		if err != nil {
			reason := "error"
			if err == ErrSpeakPermit {
				reason = "speak_permit"
			}
			en := d.dropQueuedLocked(q, reason)
			d.deviceMu.Unlock()
			en.NotifyHTTP()
			continue
		}
		_, en := d.appendEventLocked("speak_dequeued", q.turnID, "", "", "", "")
		onStarted := d.onTurnStarted
		d.deviceMu.Unlock()
		en.NotifyHTTP()
		if onStarted != nil {
			onStarted(q.turnID, uuid, seqBefore)
		}
		launch()
		return
	}
}

// dropQueuedLocked 排队项作废：写事件、登记 turnTerm、唤醒 done waiter。持 deviceMu。
func (d *DeviceInstance) dropQueuedLocked(q queuedSpeak, reason string) EventNotify {
	ev, en := d.appendEventLocked("speak_backlog_dropped", q.turnID, reason, "", "", "")
	d.turnTerm[q.turnID] = ev
	if ch := d.turnDone[q.turnID]; ch != nil {
		select {
		case ch <- ev:
		default:
		}
	}
	return en
}

// BacklogLen 当前排队数（供 GET /devices/{id}）。
func (d *DeviceInstance) BacklogLen() int {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return len(d.speakBacklog)
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
	stream := pcm
	// Phase 4f wav 推流：整段 PCM 只编码一次（加 RIFF 头）再按 slice 切流，
	// 禁止逐片封装 WAV；pace/时长仍按纯 PCM 计。
	if d.cfg.Audio.Format == "wav" {
		stream = EncodeWAV(PCM{
			Samples:       pcm,
			SampleRate:    d.cfg.Audio.SampleRate,
			Channels:      d.cfg.Audio.Channels,
			BitsPerSample: 16,
		})
	}
	frames := BuildUplinkFrames(stream, uuid, d.sampleRate, d.cfg.Audio.SliceMs, d.fault, d.cfg.Audio.MaxPayloadSize)

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

// isTurnFinal：turn_terminal 与排队作废（speak_backlog_dropped）都算 Turn 的收梢。
func isTurnFinal(ev Event) bool {
	return ev.Type == "turn_terminal" || ev.Type == "speak_backlog_dropped"
}

func (d *DeviceInstance) WaitTurn(turnID string, timeout time.Duration) (Event, error) {
	d.deviceMu.Lock()
	if ev, ok := d.turnTerm[turnID]; ok && isTurnFinal(ev) {
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
		if ev, ok := d.turnTerm[turnID]; ok && isTurnFinal(ev) {
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

// WaitBudgetForDuration 流式上行（压缩格式）：上行耗时由音频实际时长给出。
func (d *DeviceInstance) WaitBudgetForDuration(durMs int) time.Duration {
	cfg := d.Config()
	return WaitBudget(
		time.Duration(durMs)*time.Millisecond,
		seconds(cfg.Behavior.FirstReplyTimeoutSec),
		seconds(cfg.Behavior.DownlinkIdleTimeoutSec),
		seconds(cfg.Behavior.NonAudioFollowupSec),
		seconds(cfg.Behavior.PostFinalASRSilenceSec),
		seconds(cfg.Behavior.WaitTimeoutSlackSec),
	)
}
