package core

import (
	"context"
	"errors"
	"io"

	"toy-device-simulator/protocol"
)

// StreamOpen 打开一个按实际播放速率产出设备线上格式字节的流
// （典型实现：media.Toolchain.StreamRealtime，ffmpeg -re）。
// ctx 取消时实现方必须终止底层进程；返回的 ReadCloser 的 Close 同义。
type StreamOpen func(ctx context.Context) (io.ReadCloser, error)

// streamSpec 压缩格式上行的流式规格。
type streamSpec struct {
	open  StreamOpen
	durMs int
	chunk int // 每帧聚合的目标字节数（≈码率 × slice_ms）
}

// uplinkTurnStream Phase 5d：压缩格式（mp3/amr/aac）上行。
// 与 uplinkTurn 的差异：帧不预构建，从 -re 实时流边读边帧化——
// 发送节奏由 ffmpeg 限速给出，Go 侧不再 pace sleep。
// 锁检查 / 冻结 / 入队 / 排空语义与 uplinkTurn 保持一致。
func (d *DeviceInstance) uplinkTurnStream(turnID string, uuid uint32, sp streamSpec) {
	d.deviceMu.Lock()
	if d.slot.ID() != turnID || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	d.slot.SetState(TurnSpeaking)
	d.deviceMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc, err := sp.open(ctx)
	if err != nil {
		d.failStreamTurn(turnID, "uplink_source_error")
		return
	}
	defer rc.Close()

	// finalize 时立刻杀 ffmpeg，不等下一次 Read 返回。
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-d.finalizeDone:
			cancel()
			_ = rc.Close()
		case <-watchDone:
		}
	}()

	// 故障注入与 BuildUplinkFrames 对齐：bad_seq 起始 1（正常 0）。
	// 其余注入（oversize/bad_header/...）只在 pcm/wav 预构建路径支持。
	seq := uint32(0)
	if d.fault == FaultBadSeq {
		seq = 1
	}
	chunk := sp.chunk
	if chunk <= 0 {
		chunk = 1024
	}
	if mp := d.cfg.Audio.MaxPayloadSize; mp > 0 && chunk > mp {
		chunk = mp
	}
	buf := make([]byte, chunk)
	readErr := ""
	for {
		n, rerr := io.ReadAtLeast(rc, buf, len(buf))
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			if !d.sendStreamFrame(turnID, uuid, protocol.StageUploading, seq, payload) {
				return
			}
			seq++
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
				readErr = rerr.Error()
			}
			break
		}
	}
	if readErr != "" {
		d.failStreamTurn(turnID, "uplink_source_error")
		return
	}
	if !d.sendStreamFrame(turnID, uuid, protocol.StageFinished, seq, nil) {
		return
	}

	d.waitUplinkDrained(turnID)

	d.deviceMu.Lock()
	if d.slot.ID() == turnID && d.slot.State() != TurnTerminal && !d.finalizeStarted {
		d.enterWaitingReplyLocked()
	}
	d.deviceMu.Unlock()
}

// sendStreamFrame 帧化并入队一个流式上行帧；锁检查 / out 计数 / 背压语义
// 与 uplinkTurn 的循环体一致。返回 false 表示 turn 已终止，调用方停止读流。
func (d *DeviceInstance) sendStreamFrame(turnID string, uuid, stage, seq uint32, payload []byte) bool {
	raw, err := protocol.EncodeAudioFrame(
		protocol.NewAudioHeader(d.cfg.Audio.Format, stage, seq, uuid, 0, d.sampleRate), payload)
	if err != nil {
		return false
	}
	d.deviceMu.Lock()
	if d.finalizeStarted || d.slot.ID() != turnID || d.slot.State() == TurnTerminal {
		d.deviceMu.Unlock()
		return false
	}
	if d.turn != nil && d.turn.frozen {
		d.deviceMu.Unlock()
		return false
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
	qerr := d.enqueueData(Frame{
		Kind:          KindAudioData,
		TurnID:        turnID,
		UUID:          uuid,
		Stage:         stage,
		Seq:           seq,
		Raw:           raw,
		InjectedFault: injected,
		turn:          tr,
	})
	if qerr != nil {
		d.turn.out.Add(-1)
		if d.turn.out.Load() <= 0 {
			d.turn.signalDrained()
		}
		d.deviceMu.Unlock()
		if qerr == ErrDataFull {
			d.requestFinalizeAsync("write_backpressure", false)
		}
		return false
	}
	d.deviceMu.Unlock()
	return true
}

// failStreamTurn 上行源失败（ffmpeg 打不开/中途出错）时收口当前 turn：
// 冻结 + 取消（按状态决定是否补 Stage=3）+ EndError 终态，连接保留。
func (d *DeviceInstance) failStreamTurn(turnID, reason string) {
	d.deviceMu.Lock()
	if d.slot.ID() != turnID || d.slot.State() == TurnTerminal || d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	var acc []EventNotify
	_, n := d.appendEventLocked("local_validation_error", turnID, reason, "", "", "")
	acc = append(acc, n)
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
	d.deviceMu.Unlock()
	d.finishCritical(acc, tn)
}
