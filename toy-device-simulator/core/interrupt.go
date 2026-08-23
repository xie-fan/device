package core

import "fmt"

// ErrFinalizeStarted interrupt 在 finalize_started 时返回，不得 CancelTurn。
var ErrFinalizeStarted = fmt.Errorf("finalize_started")

// ErrTurnMismatch 提供了 turn_id 且槽占用且 id 不同，不得 CancelTurn。
var ErrTurnMismatch = fmt.Errorf("turn_mismatch")

type InterruptResult struct {
	Interrupted  bool
	TurnID       string
	EndReason    string
	UplinkReason string
	ReplyKind    string
}

// Interrupt 在同一 deviceMu 内读槽、比 turn_id、再 cancelTurnLocked。禁止 RequestFinalize。连接保持。
func (d *DeviceInstance) Interrupt(turnID string) (InterruptResult, error) {
	d.deviceMu.Lock()
	var acc []EventNotify
	var tn TerminalNotify
	defer func() {
		d.deviceMu.Unlock()
		d.finishCritical(acc, tn)
	}()
	if d.finalizeStarted {
		return InterruptResult{}, ErrFinalizeStarted
	}
	if !d.slot.Occupied() {
		return InterruptResult{Interrupted: false}, nil
	}
	if turnID != "" && turnID != d.slot.ID() {
		return InterruptResult{}, ErrTurnMismatch
	}
	turnID = d.slot.ID()
	st := d.slot.State()
	cr := d.cancelTurnLocked()
	if cr == CancelBackpressure {
		_, n := d.appendEventLocked("local_validation_error", turnID, "stage3_backpressure", "", "", "")
		acc = append(acc, n)
	}
	uplinkIfEmpty := ""
	if st != TurnReserved {
		uplinkIfEmpty = "interrupt"
	}
	tn = d.terminalLocked(EndInterrupt, "", uplinkIfEmpty, false)
	acc = append(acc, eventNotifyOf(tn))
	_, end, uplink, kind := d.slot.Snapshot()
	return InterruptResult{
		Interrupted:  true,
		TurnID:       turnID,
		EndReason:    end,
		UplinkReason: uplink,
		ReplyKind:    kind,
	}, nil
}
