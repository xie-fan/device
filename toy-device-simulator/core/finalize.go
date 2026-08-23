package core

import (
	"time"
)

func (d *DeviceInstance) requestFinalizeAsync(reason string, user bool) {
	go d.beginFinalize(reason, user)
}

// RequestFinalize 启动收口（后来者不等待，由 WaitFinalize join）。
func (d *DeviceInstance) RequestFinalize(reason string, user bool) {
	d.beginFinalize(reason, user)
}

func (d *DeviceInstance) WaitFinalize() {
	<-d.finalizeDone
}

func (d *DeviceInstance) Shutdown() {
	d.RequestFinalize("user_stop", true)
	d.WaitFinalize()
	if d.recorder != nil {
		d.recorder.Stop()
	}
}

func (d *DeviceInstance) beginFinalize(reason string, user bool) {
	d.deviceMu.Lock()
	if d.finalizeStarted {
		d.deviceMu.Unlock()
		return
	}
	d.finalizeStarted = true
	d.finalizeUser = user
	d.finalizeReason = reason
	d.connMu.Lock()
	d.connState = ConnDisconnecting
	d.connMu.Unlock()
	if d.turn != nil {
		d.turn.frozen = true
		d.turn.signalDrained()
	}
	d.stopKeepaliveLocked()

	send3 := false
	uuid := uint32(0)
	turnID := ""
	st := d.slot.State()
	if st != TurnEmpty && st != TurnTerminal {
		send3 = CancelSendsStage3(st)
		uuid = d.slot.UUID()
		turnID = d.slot.ID()
	}

	var acc []EventNotify
	d.connMu.Lock()
	d.writePumpMu.Lock()
	cr := d.outbound.BeginClose(uuid, turnID, send3)
	d.outbound.FillEmptyStage3(d.encodeStage3)
	tr := d.turn
	d.outbound.AttachMeta(func(f *Frame) {
		if f.Kind == KindStage3 && tr != nil {
			f.turn = tr
		}
	})
	d.writePumpCond.Broadcast()
	d.writePumpMu.Unlock()
	d.connMu.Unlock()
	if cr == CloseBackpressure {
		_, n := d.appendEventLocked("local_validation_error", turnID, "stage3_backpressure", "", "", "")
		acc = append(acc, n)
	}
	d.deviceMu.Unlock()
	d.finishCritical(acc, TerminalNotify{})
	go d.closer()
}

func (d *DeviceInstance) stopKeepaliveLocked() {
	d.keepOnce.Do(func() {
		close(d.keepStop)
	})
}

func (d *DeviceInstance) closer() {
	d.phaseB()
	d.phaseC()
}

func (d *DeviceInstance) phaseB() {
	deadline := time.Now().Add(d.drainTimeout)
	for time.Now().Before(deadline) {
		d.writePumpMu.Lock()
		empty := d.outbound.Len() == 0 && d.outbound.InFlight() == nil
		d.writePumpMu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	d.connMu.Lock()
	conn := d.conn
	d.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}

	d.writePumpMu.Lock()
	d.writePumpCond.Broadcast()
	d.writePumpMu.Unlock()

	d.deviceMu.Lock()
	ch := d.readLoopDone
	d.deviceMu.Unlock()
	if ch != nil {
		<-ch
	}
}

func (d *DeviceInstance) phaseC() {
	var acc []EventNotify
	var tn TerminalNotify
	d.deviceMu.Lock()
	d.finalizeCommitted = true
	d.stopPendingReportsLocked()
	if d.slot.Occupied() {
		uplinkIfEmpty := ""
		if d.slot.State() != TurnReserved {
			uplinkIfEmpty = "error"
		}
		tn = d.terminalLocked(EndConnectionLost, "", uplinkIfEmpty, true)
		acc = append(acc, eventNotifyOf(tn))
	}
	d.connMu.Lock()
	d.connState = ConnDisconnected
	d.connMu.Unlock()
	if d.finalizeUser {
		_, n := d.appendEventLocked("connection_stopped", "", "", "", "", "")
		acc = append(acc, n)
	} else {
		_, n := d.appendEventLocked("connection_failed", "", d.finalizeReason, "", "", "")
		acc = append(acc, n)
	}
	speak := d.takeSpeakableWaitersLocked()
	close(d.finalizeDone)
	d.deviceMu.Unlock()
	notifySpeakable(speak, 409, ConnDisconnected)
	d.finishCritical(acc, tn)
}
