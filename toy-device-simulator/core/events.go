package core

import (
	"encoding/json"
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

type EventNotify struct {
	EventWaiters int
	SlowSubs     int
}

type EventLog struct {
	deviceID   string
	instanceID string
	seq        int
	events     []Event
}

func NewEventLog(deviceID, instanceID string) *EventLog {
	return &EventLog{deviceID: deviceID, instanceID: instanceID}
}

func (l *EventLog) Seq() int { return l.seq }

func (l *EventLog) AppendLocked(typ string, turnID, reason, endReason, uplinkReason, replyKind string) (Event, EventNotify) {
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
	return ev, EventNotify{} // Phase 1：HTTP waiter / hub 恒空
}

func (l *EventLog) Snapshot() []Event {
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

func (l *EventLog) Types() []string {
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
