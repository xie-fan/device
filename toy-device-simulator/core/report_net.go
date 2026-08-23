package core

import (
	"fmt"
	"time"

	"toy-device-simulator/protocol"
)

func (d *DeviceInstance) sendReport(kind string) int {
	d.deviceMu.Lock()
	if d.finalizeStarted || d.finalizeCommitted || d.deleted {
		d.deviceMu.Unlock()
		return 0
	}
	d.connMu.Lock()
	if kind == "initial" {
		d.connState = ConnReporting
	}
	d.connMu.Unlock()
	playingMode := d.cfg.PlayingMode
	seq := d.registerPendingLocked(kind)
	var n EventNotify
	if kind == "initial" {
		_, n = d.appendEventLocked("reporting", "", "", "", "", "")
	}
	d.deviceMu.Unlock()
	if kind == "initial" {
		d.finishCritical([]EventNotify{n}, TerminalNotify{})
	}

	raw, err := d.encodeReport(seq, playingMode)
	if err != nil {
		d.requestFinalizeAsync("report_encode", false)
		return seq
	}
	d.enqueueOrFinalize(Frame{Kind: KindReport, Raw: raw, Seq: uint32(seq)})
	return seq
}

func (d *DeviceInstance) encodeReport(seq, playingMode int) ([]byte, error) {
	return protocol.EncodeManage(
		protocol.Topic(d.cfg.Enterprise, d.cfg.DeviceType, d.cfg.DeviceID, "report", "server"),
		protocol.ReportData{
			SequenceNumber: seq,
			Code:           0,
			SignalStrength: 0,
			BatPowerLevel:  100,
			PlayingMode:    playingMode,
		},
	)
}

func (d *DeviceInstance) registerPendingLocked(kind string) int {
	d.reportMu.Lock()
	defer d.reportMu.Unlock()
	seq := d.reports.TakeLocked()
	timeout := seconds(d.cfg.Behavior.ReportEchoTimeoutSec)
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	meta := &pendingMeta{kind: kind}
	meta.timer = time.AfterFunc(timeout, func() { d.onReportTimeout(seq) })
	d.pendingMeta[seq] = meta
	return seq
}

func (d *DeviceInstance) stopPendingReportsLocked() {
	d.reportMu.Lock()
	defer d.reportMu.Unlock()
	for seq, meta := range d.pendingMeta {
		if meta != nil {
			if meta.timer != nil {
				meta.timer.Stop()
			}
			meta.once.TryConsume()
		}
		delete(d.pendingMeta, seq)
		d.reports.Ack(seq)
	}
}

// ManualReport 仅 Ready；可选热更 playingMode。更新 playing_mode 与登记 pending 同一临界区。
func (d *DeviceInstance) ManualReport(playingMode *int) (int, error) {
	d.deviceMu.Lock()
	d.connMu.Lock()
	st := d.connState
	d.connMu.Unlock()
	if st != ConnReady || d.finalizeStarted || d.finalizeCommitted || d.deleted {
		d.deviceMu.Unlock()
		return 0, fmt.Errorf("仅 Ready 可 POST /report")
	}
	if playingMode != nil {
		d.cfg.PlayingMode = *playingMode
	}
	playing := d.cfg.PlayingMode
	seq := d.registerPendingLocked("manual")
	d.deviceMu.Unlock()

	raw, err := d.encodeReport(seq, playing)
	if err != nil {
		d.requestFinalizeAsync("report_encode", false)
		return seq, nil
	}
	d.enqueueOrFinalize(Frame{Kind: KindReport, Raw: raw, Seq: uint32(seq)})
	return seq, nil
}

func (d *DeviceInstance) onReportTimeout(seq int) {
	var acc []EventNotify
	var finalize bool
	d.deviceMu.Lock()
	d.reportMu.Lock()
	meta, ok := d.pendingMeta[seq]
	if !ok || !meta.once.TryConsume() {
		d.reportMu.Unlock()
		d.deviceMu.Unlock()
		return
	}
	delete(d.pendingMeta, seq)
	d.reports.Ack(seq)
	kind := meta.kind
	d.reportMu.Unlock()
	if d.finalizeStarted || d.finalizeCommitted || d.deleted {
		d.deviceMu.Unlock()
		return
	}
	_, n := d.appendEventLocked("report_timeout", "", "", "", "", "")
	acc = append(acc, n)
	d.connMu.Lock()
	if kind == "initial" && d.connState == ConnReporting {
		finalize = true
	}
	d.connMu.Unlock()
	d.deviceMu.Unlock()
	d.finishCritical(acc, TerminalNotify{})
	if finalize {
		select {
		case d.readyDone <- fmtReportTimeout():
		default:
		}
		d.requestFinalizeAsync("report_timeout", false)
	}
}

func fmtReportTimeout() error { return errReportTimeout }

var errReportTimeout = errString("初始 report 回显超时")

// ErrWaitTimeout 表示 --wait 预算耗尽且尚未收到 turn_terminal。
var ErrWaitTimeout = errString("等待 Turn 完成超时")

type errString string

func (e errString) Error() string { return string(e) }

func (e errString) Is(target error) bool {
	o, ok := target.(errString)
	return ok && e == o
}

func (d *DeviceInstance) handleReportEchoLocked(seq int) (acc []EventNotify, ready bool) {
	d.reportMu.Lock()
	meta, ok := d.pendingMeta[seq]
	if !ok {
		d.reportMu.Unlock()
		_, n := d.appendEventLocked("report_echo_unmatched", "", "", "", "", "")
		return []EventNotify{n}, false
	}
	if !meta.once.TryConsume() {
		d.reportMu.Unlock()
		return nil, false
	}
	if meta.timer != nil {
		meta.timer.Stop()
	}
	kind := meta.kind
	delete(d.pendingMeta, seq)
	d.reports.Ack(seq)
	d.reportMu.Unlock()

	_, n := d.appendEventLocked("report_echo", "", "", "", "", "")
	acc = append(acc, n)
	d.connMu.Lock()
	if kind == "initial" && d.connState == ConnReporting {
		d.connState = ConnReady
		d.connMu.Unlock()
		_, n2 := d.appendEventLocked("ready", "", "", "", "", "")
		acc = append(acc, n2)
		select {
		case d.readyDone <- nil:
		default:
		}
		return acc, true
	}
	d.connMu.Unlock()
	return acc, false
}

func (d *DeviceInstance) startKeepalive() {
	interval := seconds(d.cfg.Behavior.KeepaliveIntervalSec)
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				d.sendKeepalive()
			case <-d.keepStop:
				return
			case <-d.finalizeDone:
				return
			}
		}
	}()
}

func (d *DeviceInstance) sendKeepalive() {
	d.deviceMu.Lock()
	d.connMu.Lock()
	ok := KeepaliveEnabled(d.connState, d.fault == FaultSkipReport)
	d.connMu.Unlock()
	d.deviceMu.Unlock()
	if !ok {
		return
	}
	d.sendReport("keepalive")
}
