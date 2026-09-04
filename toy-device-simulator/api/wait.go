package api

import (
	"encoding/json"
	"net/http"
	"time"

	"toy-device-simulator/core"
)

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID       string   `json:"device_id"`
		InstanceID     string   `json:"instance_id"`
		TurnID         string   `json:"turn_id"`
		EventType      string   `json:"event_type"`
		AfterEventSeq  *int     `json:"after_event_seq"`
		TimeoutSec     *float64 `json:"timeout_sec"`
		ConnGeneration *int     `json:"conn_generation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if body.DeviceID == "" || body.InstanceID == "" {
		writeErr(w, http.StatusBadRequest, "缺 device_id/instance_id")
		return
	}
	if body.TurnID == "" && body.EventType == "" {
		writeErr(w, http.StatusBadRequest, "turn_id 或 event_type 至少一个")
		return
	}
	after := 0
	if body.AfterEventSeq != nil {
		after = *body.AfterEventSeq
	}
	timeout := waitReadyDefault(s.opts.Config)
	if body.TimeoutSec != nil {
		timeout = time.Duration(*body.TimeoutSec * float64(time.Second))
	}

	gen := 0
	if body.ConnGeneration != nil {
		gen = *body.ConnGeneration
	}

	if body.EventType == "speakable" {
		if body.ConnGeneration == nil {
			writeErr(w, http.StatusBadRequest, "speakable 必填 conn_generation")
			return
		}
		code, payload := s.waitReady(body.DeviceID, body.InstanceID, gen, timeout)
		writeJSON(w, code, payload)
		return
	}

	s.mu.Lock()
	code, payload, proceed := s.gateWaitGenerationLocked(body.DeviceID, body.InstanceID, body.ConnGeneration)
	s.mu.Unlock()
	if !proceed {
		writeJSON(w, code, payload)
		return
	}

	code, payload = s.waitEvent(body.DeviceID, body.InstanceID, body.EventType, body.TurnID, after, timeout, gen)
	writeJSON(w, code, payload)
}

// gateWaitGenerationLocked：live 且当前 speakable（或请求带了 generation）时缺 conn_generation → 400；
// 带了但与当前代 / committed / tombstone 不符 → 409 generation_gone。
func (s *Server) gateWaitGenerationLocked(deviceID, instanceID string, genp *int) (int, any, bool) {
	live, tomb, found := s.resolveLiveLocked(deviceID, instanceID)
	speakable := live != nil && live.inst != nil && core.Speakable(live.inst.ConnectionState(), live.fault)
	needGen := speakable || genp != nil
	if !needGen {
		return 0, nil, true
	}
	if genp == nil {
		return http.StatusBadRequest, map[string]any{"error": "speakable 时必填 conn_generation"}, false
	}
	gen := *genp
	if found == "" {
		return http.StatusNotFound, map[string]any{"error": "instance 未命中"}, false
	}
	if tomb != nil {
		if tomb.gen != gen {
			return http.StatusConflict, map[string]any{"error": "generation_gone"}, false
		}
		return 0, nil, true
	}
	if live.committed[gen] {
		return http.StatusConflict, map[string]any{"error": "generation_gone"}, false
	}
	if live.inst != nil && live.inst.FinalizeCommitted() && live.gen == gen {
		live.committed[gen] = true
		return http.StatusConflict, map[string]any{"error": "generation_gone"}, false
	}
	if live.gen != gen {
		return http.StatusConflict, map[string]any{"error": "generation_gone"}, false
	}
	return 0, nil, true
}

func (s *Server) waitEvent(deviceID, instanceID, eventType, turnID string, after int, timeout time.Duration, gen int) (int, any) {
	s.mu.Lock()
	live, tomb, found := s.resolveLiveLocked(deviceID, instanceID)
	if found == "" {
		s.mu.Unlock()
		return http.StatusNotFound, map[string]any{"error": "instance 未命中"}
	}
	var log *core.EventLog
	var inst *core.DeviceInstance
	isTomb := tomb != nil
	if live != nil {
		log = live.log
		inst = live.inst
	} else {
		log = tomb.log
	}
	if log.CursorExpired(after) {
		payload := map[string]any{
			"error":               "event_seq_expired",
			"evicted_through_seq": log.EvictedThrough(),
			"oldest_seq":          log.OldestSeq(),
			"newest_seq":          log.NewestSeq(),
		}
		s.mu.Unlock()
		return http.StatusGone, payload
	}
	if isTomb {
		if ev, ok := findEvent(log.After(after), eventType, turnID); ok {
			s.mu.Unlock()
			return http.StatusOK, eventWaitJSON(ev)
		}
		s.mu.Unlock()
		return http.StatusNotFound, map[string]any{"error": "tombstone 历史未命中"}
	}

	var ev core.Event
	var ch chan core.Event
	expired, hit := false, false
	if inst != nil {
		ev, ch, expired, hit = inst.OfferEventWait(after, eventType, turnID, gen)
	} else {
		ev, ch, expired, hit = log.FindOrRegisterWaiter(after, eventType, turnID, gen)
	}
	s.mu.Unlock()
	if expired {
		return http.StatusGone, map[string]any{"error": "event_seq_expired"}
	}
	if hit {
		return http.StatusOK, eventWaitJSON(ev)
	}

	if timeout <= 0 {
		timeout = waitReadyDefault(s.opts.Config)
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case got := <-ch:
		if gen != 0 && got.ConnGeneration != gen {
			return http.StatusConflict, map[string]any{"error": "generation_gone"}
		}
		return http.StatusOK, eventWaitJSON(got)
	case <-t.C:
		return s.waitEventTimeout(deviceID, instanceID, log, inst, ch, gen)
	}
}

func (s *Server) waitEventTimeout(deviceID, instanceID string, log *core.EventLog, inst *core.DeviceInstance, ch chan core.Event, gen int) (int, any) {
	still := log.RemoveWaiter(ch)
	if !still {
		got := <-ch
		if gen != 0 && got.ConnGeneration != gen {
			return http.StatusConflict, map[string]any{"error": "generation_gone"}
		}
		return http.StatusOK, eventWaitJSON(got)
	}
	s.mu.Lock()
	live, tomb, found := s.resolveLiveLocked(deviceID, instanceID)
	s.mu.Unlock()
	if found == "" || tomb != nil {
		return http.StatusNotFound, map[string]any{"error": "tombstone 历史未命中"}
	}
	if gen != 0 && live != nil && (live.gen != gen || live.committed[gen]) {
		return http.StatusConflict, map[string]any{"error": "generation_gone"}
	}
	if inst != nil && inst.FinalizeCommitted() {
		return http.StatusConflict, map[string]any{"error": "generation_gone"}
	}
	return http.StatusGatewayTimeout, map[string]any{"error": "wait 超时"}
}

func findEvent(evs []core.Event, typ, turnID string) (core.Event, bool) {
	for _, e := range evs {
		if typ != "" && e.Type != typ {
			continue
		}
		if turnID != "" && e.TurnID != turnID {
			continue
		}
		return e, true
	}
	return core.Event{}, false
}

func eventWaitJSON(e core.Event) map[string]any {
	m := map[string]any{
		"device_id":         e.DeviceID,
		"instance_id":       e.InstanceID,
		"turn_id":           e.TurnID,
		"event_seq":         e.EventSeq,
		"event_type":        e.Type,
		"turn_end_reason":   e.EndReason,
		"uplink_end_reason": e.UplinkReason,
		"reply_kind":        e.ReplyKind,
		"ts":                e.At.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if e.PayloadLen > 0 {
		m["payload_len"] = e.PayloadLen
	}
	return m
}
