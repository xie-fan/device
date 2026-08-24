package core

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	DeviceID     string    `json:"device_id"`
	InstanceID   string    `json:"instance_id"`
	EventSeq     int       `json:"event_seq"`
	Type         string    `json:"event_type"`
	TurnID       string    `json:"turn_id,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	EndReason    string    `json:"turn_end_reason,omitempty"`
	UplinkReason string    `json:"uplink_end_reason,omitempty"`
	ReplyKind    string    `json:"reply_kind,omitempty"`
	At           time.Time `json:"-"`
}

func (e Event) MarshalJSON() ([]byte, error) {
	type wire struct {
		DeviceID     string `json:"device_id"`
		InstanceID   string `json:"instance_id"`
		EventSeq     int    `json:"event_seq"`
		Type         string `json:"event_type"`
		TS           string `json:"ts"`
		TurnID       string `json:"turn_id,omitempty"`
		Reason       string `json:"reason,omitempty"`
		EndReason    string `json:"turn_end_reason,omitempty"`
		UplinkReason string `json:"uplink_end_reason,omitempty"`
		ReplyKind    string `json:"reply_kind,omitempty"`
	}
	ts := e.At.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return json.Marshal(wire{
		DeviceID:     e.DeviceID,
		InstanceID:   e.InstanceID,
		EventSeq:     e.EventSeq,
		Type:         e.Type,
		TS:           ts,
		TurnID:       e.TurnID,
		Reason:       e.Reason,
		EndReason:    e.EndReason,
		UplinkReason: e.UplinkReason,
		ReplyKind:    e.ReplyKind,
	})
}

type waiterWake struct {
	ch chan Event
	ev Event
}

type EventNotify struct {
	EventWaiters int
	SlowSubs     int
	wakes        []waiterWake
}

func (n EventNotify) NotifyHTTP() {
	for _, w := range n.wakes {
		if w.ch == nil {
			continue
		}
		select {
		case w.ch <- w.ev:
		default:
		}
	}
}

func eventNotifyOf(tn TerminalNotify) EventNotify {
	return EventNotify{EventWaiters: tn.EventWaiters, SlowSubs: tn.SlowSubs, wakes: tn.wakes}
}

type eventWaiter struct {
	eventType string
	turnID    string
	afterSeq  int
	ch        chan Event
}

type EventLog struct {
	mu             sync.Mutex
	deviceID       string
	instanceID     string
	seq            int
	events         []Event
	maxEntries     int // 0=不限制，避免破坏 Phase 1
	evictedThrough int
	waiters        []*eventWaiter
	hubSubs        map[int]*WSSub
	hubNext        int
}

func NewEventLog(deviceID, instanceID string) *EventLog {
	return &EventLog{deviceID: deviceID, instanceID: instanceID}
}

// SetMaxEntries 0 表示不淘汰。
func (l *EventLog) SetMaxEntries(n int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxEntries = n
	l.evictLocked()
}

func (l *EventLog) Seq() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

func (l *EventLog) EvictedThrough() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.evictedThrough
}

func (l *EventLog) OldestSeq() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) == 0 {
		return l.evictedThrough + 1
	}
	return l.events[0].EventSeq
}

func (l *EventLog) NewestSeq() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// CursorExpired 当且仅当 after < evicted_through_seq。
func (l *EventLog) CursorExpired(after int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return after < l.evictedThrough
}

func eventMatches(e Event, typ, turnID string) bool {
	if typ != "" && e.Type != typ {
		return false
	}
	if turnID != "" && e.TurnID != turnID {
		return false
	}
	return true
}

func (l *EventLog) AppendLocked(typ string, turnID, reason, endReason, uplinkReason, replyKind string) (Event, EventNotify) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	ev := Event{
		DeviceID:     l.deviceID,
		InstanceID:   l.instanceID,
		EventSeq:     l.seq,
		Type:         typ,
		TurnID:       turnID,
		Reason:       reason,
		EndReason:    endReason,
		UplinkReason: uplinkReason,
		ReplyKind:    replyKind,
		At:           time.Now().UTC(),
	}
	l.events = append(l.events, ev)
	l.evictLocked()
	n := EventNotify{}
	rest := l.waiters[:0]
	for _, w := range l.waiters {
		if w == nil {
			continue
		}
		if ev.EventSeq > w.afterSeq && eventMatches(ev, w.eventType, w.turnID) {
			n.EventWaiters++
			n.wakes = append(n.wakes, waiterWake{ch: w.ch, ev: ev})
			continue
		}
		rest = append(rest, w)
	}
	l.waiters = rest
	n.SlowSubs += l.deliverHubLocked(ev)
	return ev, n
}

// FindOrRegisterWaiter 检查历史与登记同一临界区。命中不登记。
func (l *EventLog) FindOrRegisterWaiter(after int, typ, turnID string) (ev Event, ch chan Event, expired, hit bool) {
	if l == nil {
		return Event{}, nil, false, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if after < l.evictedThrough {
		return Event{}, nil, true, false
	}
	for _, e := range l.events {
		if e.EventSeq > after && eventMatches(e, typ, turnID) {
			return e, nil, false, true
		}
	}
	ch = make(chan Event, 1)
	l.waiters = append(l.waiters, &eventWaiter{
		eventType: typ,
		turnID:    turnID,
		afterSeq:  after,
		ch:        ch,
	})
	return Event{}, ch, false, false
}

// RemoveWaiter 超时试消费：仍在表中则摘掉并返回 true。
func (l *EventLog) RemoveWaiter(ch chan Event) bool {
	if l == nil || ch == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, w := range l.waiters {
		if w != nil && w.ch == ch {
			l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
			return true
		}
	}
	return false
}

func (l *EventLog) WaiterCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiters)
}

func (l *EventLog) MaxEntries() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxEntries
}

func (l *EventLog) evictLocked() {
	if l.maxEntries <= 0 {
		return
	}
	for len(l.events) > l.maxEntries {
		l.evictedThrough = l.events[0].EventSeq
		l.events = l.events[1:]
	}
}

func (l *EventLog) Snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

func (l *EventLog) After(after int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.afterLocked(after)
}

func (l *EventLog) afterLocked(after int) []Event {
	var out []Event
	for _, e := range l.events {
		if e.EventSeq > after {
			out = append(out, e)
		}
	}
	return out
}

func (l *EventLog) Types() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.events))
	for _, e := range l.events {
		out = append(out, e.Type)
	}
	return out
}

var forbiddenServerLogNames = map[string]bool{
	"device_not_found":  true,
	"status_invalid":    true,
	"no_active_turn":    true,
	"inferred_no_reply": true, // 只保留 expected_server_drop
}

func ForbiddenEventType(typ string) bool { return forbiddenServerLogNames[typ] }
