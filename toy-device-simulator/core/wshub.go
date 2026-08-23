package core

import (
	"time"
)

const (
	WSOpen int = iota
	WSDrain
	WSAbort
)

const (
	WSCatchup int = iota
	WSLivePhase
)

const wsLiveInboxCap = 256

// WSSub 是 live 事件 WS 的订阅。close_mode / phase / last_pong 只在 device_mu 内改。
type WSSub struct {
	id        int
	turnID    string
	afterSeq  int
	backlog   []Event
	inbox     chan Event
	closeMode int
	phase     int
	lastPong  time.Time
	closed    bool
}

func (s *WSSub) Backlog() []Event {
	if s == nil {
		return nil
	}
	return s.backlog
}

func (s *WSSub) Inbox() <-chan Event {
	if s == nil {
		return nil
	}
	return s.inbox
}

func (d *DeviceInstance) RegisterWS(after int, turnID string) (*WSSub, bool) {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	if d.events.CursorExpired(after) {
		return nil, false
	}
	return d.registerWSLocked(after, turnID), true
}

func (d *DeviceInstance) registerWSLocked(after int, turnID string) *WSSub {
	backlog := d.events.After(after)
	if turnID != "" {
		filtered := make([]Event, 0, len(backlog))
		for _, e := range backlog {
			if e.TurnID == turnID {
				filtered = append(filtered, e)
			}
		}
		backlog = filtered
	}
	capN := d.events.MaxEntries()
	if capN <= 0 {
		capN = 10000
	}
	d.hubNext++
	sub := &WSSub{
		id:        d.hubNext,
		turnID:    turnID,
		afterSeq:  after,
		backlog:   backlog,
		inbox:     make(chan Event, capN),
		closeMode: WSOpen,
		phase:     WSCatchup,
		lastPong:  time.Now(),
	}
	if d.hubSubs == nil {
		d.hubSubs = map[int]*WSSub{}
	}
	d.hubSubs[sub.id] = sub
	return sub
}

func (d *DeviceInstance) RequestClose(sub *WSSub, mode int) {
	if d == nil || sub == nil {
		return
	}
	d.deviceMu.Lock()
	d.requestCloseLocked(sub, mode)
	d.deviceMu.Unlock()
}

func (d *DeviceInstance) requestCloseLocked(sub *WSSub, mode int) {
	if sub == nil {
		return
	}
	if mode == WSAbort {
		sub.closeMode = WSAbort
	} else if sub.closeMode == WSOpen && mode == WSDrain {
		sub.closeMode = WSDrain
	}
	delete(d.hubSubs, sub.id)
	if !sub.closed {
		sub.closed = true
		close(sub.inbox)
	}
}

func (d *DeviceInstance) requestCloseAllLocked(mode int) {
	subs := make([]*WSSub, 0, len(d.hubSubs))
	for _, sub := range d.hubSubs {
		subs = append(subs, sub)
	}
	for _, sub := range subs {
		d.requestCloseLocked(sub, mode)
	}
}

func (d *DeviceInstance) NotePong(sub *WSSub) {
	if d == nil || sub == nil {
		return
	}
	d.deviceMu.Lock()
	sub.lastPong = time.Now()
	d.deviceMu.Unlock()
}

func (d *DeviceInstance) LastPong(sub *WSSub) time.Time {
	if d == nil || sub == nil {
		return time.Time{}
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return sub.lastPong
}

func (d *DeviceInstance) WSCloseMode(sub *WSSub) int {
	if d == nil || sub == nil {
		return WSAbort
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return sub.closeMode
}

func (d *DeviceInstance) drainWSInboxLocked(sub *WSSub) (events []Event, closed bool) {
	for {
		select {
		case ev, ok := <-sub.inbox:
			if !ok {
				return events, true
			}
			events = append(events, ev)
		default:
			return events, false
		}
	}
}

// CatchupWS 持锁排空 inbox；空且 open 才置 live。
func (d *DeviceInstance) CatchupWS(sub *WSSub) (events []Event, abort, drainDone, live bool) {
	if d == nil || sub == nil {
		return nil, true, false, false
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	if sub.closeMode == WSAbort {
		return nil, true, false, false
	}
	events, inboxClosed := d.drainWSInboxLocked(sub)
	if len(events) > 0 {
		return events, false, false, false
	}
	if sub.closeMode == WSDrain || inboxClosed {
		return nil, false, true, false
	}
	sub.phase = WSLivePhase
	return nil, false, false, true
}

// PollWSLive 持锁看 close_mode 并排空 inbox；空则 idle=true。
func (d *DeviceInstance) PollWSLive(sub *WSSub) (events []Event, abort, drainDone, idle bool) {
	if d == nil || sub == nil {
		return nil, true, false, false
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	if sub.closeMode == WSAbort {
		return nil, true, false, false
	}
	events, inboxClosed := d.drainWSInboxLocked(sub)
	if sub.closeMode == WSDrain {
		if len(events) == 0 {
			return nil, false, true, false
		}
		return events, false, false, false
	}
	if inboxClosed && len(events) == 0 {
		return nil, false, true, false
	}
	if len(events) > 0 {
		return events, false, false, false
	}
	return nil, false, false, true
}

func (d *DeviceInstance) MarkWSPingLocked(sub *WSSub) (ok bool, abort bool, skip bool) {
	if sub.closeMode != WSOpen {
		if sub.closeMode == WSAbort {
			return false, true, false
		}
		return false, false, true
	}
	if len(sub.inbox) > 0 || sub.closed {
		return false, false, true
	}
	return true, false, false
}

func (d *DeviceInstance) PrepareIdlePing(sub *WSSub) (doPing, abort, skip bool) {
	if d == nil || sub == nil {
		return false, true, false
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	ok, abort, skip := d.MarkWSPingLocked(sub)
	return ok, abort, skip
}

func (d *DeviceInstance) IdleDeadlineAbort(sub *WSSub, pingAt, lastPong time.Time) (abortNow, skip, reopen bool) {
	if d == nil || sub == nil {
		return true, false, false
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	if sub.closeMode == WSAbort {
		return true, false, false
	}
	if sub.closeMode == WSDrain || len(sub.inbox) > 0 || sub.closed {
		return false, true, false
	}
	if pingAt.IsZero() || !lastPong.Before(pingAt) {
		return false, false, true
	}
	d.requestCloseLocked(sub, WSAbort)
	return true, false, false
}

func (d *DeviceInstance) deliverHubLocked(ev Event) int {
	slow := 0
	var overflow []*WSSub
	for _, sub := range d.hubSubs {
		if sub == nil || sub.closed {
			continue
		}
		if ev.EventSeq <= sub.afterSeq {
			continue
		}
		if sub.turnID != "" && ev.TurnID != sub.turnID {
			continue
		}
		limit := wsLiveInboxCap
		if sub.phase == WSCatchup {
			limit = cap(sub.inbox)
			if limit <= 0 {
				limit = 10000
			}
		}
		if len(sub.inbox) >= limit {
			overflow = append(overflow, sub)
			continue
		}
		select {
		case sub.inbox <- ev:
		default:
			overflow = append(overflow, sub)
		}
	}
	for _, sub := range overflow {
		d.requestCloseLocked(sub, WSAbort)
		slow++
	}
	return slow
}

// EmitDeleted 写入 device_deleted 并对 hub drain。调用方未持 device_mu。
func (d *DeviceInstance) EmitDeleted() EventNotify {
	if d == nil {
		return EventNotify{}
	}
	d.deviceMu.Lock()
	d.deleted = true
	d.stopPendingReportsLocked()
	_, n := d.appendEventLocked("device_deleted", "", "", "", "", "")
	d.requestCloseAllLocked(WSDrain)
	d.deviceMu.Unlock()
	return n
}

type SpeakableResult struct {
	Code  int
	State ConnState
}

func notifySpeakable(chs []chan SpeakableResult, code int, state ConnState) {
	if len(chs) == 0 || code == 0 {
		return
	}
	res := SpeakableResult{Code: code, State: state}
	for _, ch := range chs {
		if ch == nil {
			continue
		}
		select {
		case ch <- res:
		default:
		}
	}
}

func (d *DeviceInstance) takeSpeakableWaitersLocked() []chan SpeakableResult {
	out := d.speakableWaiters
	d.speakableWaiters = nil
	return out
}

func (d *DeviceInstance) maybeTakeSpeakableLocked() (chs []chan SpeakableResult, code int, st ConnState) {
	if d.finalizeCommitted {
		return nil, 0, ConnDisconnected
	}
	d.connMu.Lock()
	st = d.connState
	d.connMu.Unlock()
	if !Speakable(st, d.fault) {
		return nil, 0, st
	}
	return d.takeSpeakableWaitersLocked(), 200, st
}

func (d *DeviceInstance) OfferSpeakableWait() (code int, state ConnState, ch chan SpeakableResult) {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	if d.finalizeCommitted {
		d.connMu.Lock()
		st := d.connState
		d.connMu.Unlock()
		return 409, st, nil
	}
	d.connMu.Lock()
	st := d.connState
	d.connMu.Unlock()
	if Speakable(st, d.fault) {
		return 200, st, nil
	}
	ch = make(chan SpeakableResult, 1)
	d.speakableWaiters = append(d.speakableWaiters, ch)
	return 0, st, ch
}

func (d *DeviceInstance) RemoveSpeakableWaiter(ch chan SpeakableResult) bool {
	if d == nil || ch == nil {
		return false
	}
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	for i, w := range d.speakableWaiters {
		if w == ch {
			d.speakableWaiters = append(d.speakableWaiters[:i], d.speakableWaiters[i+1:]...)
			return true
		}
	}
	return false
}

func (d *DeviceInstance) OfferEventWait(after int, typ, turnID string) (ev Event, ch chan Event, expired, hit bool) {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.events.FindOrRegisterWaiter(after, typ, turnID)
}
