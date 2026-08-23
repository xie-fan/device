package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"toy-device-simulator/core"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) handleWSEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	deviceID := q.Get("device_id")
	instanceID := q.Get("instance_id")
	if deviceID == "" || instanceID == "" {
		writeErr(w, http.StatusBadRequest, "缺 device_id/instance_id")
		return
	}
	after := 0
	if raw := q.Get("after_event_seq"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "after_event_seq 非法")
			return
		}
		after = n
	}
	turnID := q.Get("turn_id")
	t := drainTimeout(s.opts.Config)

	s.mu.Lock()
	live, tomb, found := s.resolveInstance(deviceID, instanceID)
	if found == "" {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	var log *core.EventLog
	isTomb := tomb != nil
	var inst *core.DeviceInstance
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
		writeJSON(w, http.StatusGone, payload)
		return
	}

	if isTomb || inst == nil {
		backlog := log.After(after)
		if turnID != "" {
			var filtered []core.Event
			for _, e := range backlog {
				if e.TurnID == turnID {
					filtered = append(filtered, e)
				}
			}
			backlog = filtered
		}
		s.mu.Unlock()
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		writeTombstoneWS(conn, backlog, t)
		return
	}

	sub, ok := inst.RegisterWS(after, turnID)
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusGone, map[string]any{"error": "event_seq_expired"})
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		inst.RequestClose(sub, core.WSAbort)
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	go readEventWS(conn, inst, sub)
	writeLiveWS(conn, inst, sub, t)
}

func writeTombstoneWS(conn *websocket.Conn, backlog []core.Event, t time.Duration) {
	defer conn.Close()
	for _, ev := range backlog {
		_ = conn.SetWriteDeadline(time.Now().Add(t))
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}

func readEventWS(conn *websocket.Conn, inst *core.DeviceInstance, sub *core.WSSub) {
	conn.SetPongHandler(func(string) error {
		inst.NotePong(sub)
		return nil
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			inst.RequestClose(sub, core.WSAbort)
			return
		}
	}
}

func writeLiveWS(conn *websocket.Conn, inst *core.DeviceInstance, sub *core.WSSub, t time.Duration) {
	for _, ev := range sub.Backlog() {
		if err := writeOneWS(conn, inst, sub, ev, t); err != nil {
			return
		}
	}
	for {
		events, abort, drainDone, live := inst.CatchupWS(sub)
		if abort {
			finishAbortWS(conn, sub)
			return
		}
		for _, ev := range events {
			if err := writeOneWS(conn, inst, sub, ev, t); err != nil {
				return
			}
		}
		if drainDone {
			_ = conn.Close()
			return
		}
		if live {
			break
		}
	}
	writeLiveLoop(conn, inst, sub, t)
}

func writeLiveLoop(conn *websocket.Conn, inst *core.DeviceInstance, sub *core.WSSub, t time.Duration) {
	for {
		events, abort, drainDone, idle := inst.PollWSLive(sub)
		if abort {
			finishAbortWS(conn, sub)
			return
		}
		if drainDone {
			_ = conn.Close()
			return
		}
		if len(events) > 0 {
			for _, ev := range events {
				if err := writeOneWS(conn, inst, sub, ev, t); err != nil {
					return
				}
			}
			continue
		}
		if !idle {
			continue
		}
		if !waitIdleWS(conn, inst, sub, t) {
			return
		}
	}
}

func waitIdleWS(conn *websocket.Conn, inst *core.DeviceInstance, sub *core.WSSub, t time.Duration) bool {
	idleStart := time.Now()
	idleDeadline := idleStart.Add(t)
	pingAt := time.Time{}
	tHalf := time.NewTimer(t / 2)
	defer tHalf.Stop()
	tEnd := time.NewTimer(t)
	defer tEnd.Stop()
	inbox := sub.Inbox()
	for {
		select {
		case ev, ok := <-inbox:
			if !ok {
				if inst.WSCloseMode(sub) == core.WSAbort {
					finishAbortWS(conn, sub)
					return false
				}
				_ = conn.Close()
				return false
			}
			if err := writeOneWS(conn, inst, sub, ev, t); err != nil {
				return false
			}
			return true
		case <-tHalf.C:
			if !pingAt.IsZero() {
				continue
			}
			doPing, abort, skip := inst.PrepareIdlePing(sub)
			if abort {
				finishAbortWS(conn, sub)
				return false
			}
			if skip {
				return true
			}
			if !doPing {
				return true
			}
			pingAt = time.Now()
			if err := conn.WriteControl(websocket.PingMessage, nil, idleDeadline); err != nil {
				abortWS(conn, inst, sub)
				return false
			}
			if !tHalf.Stop() {
				select {
				case <-tHalf.C:
				default:
				}
			}
		case <-tEnd.C:
			lastPong := inst.LastPong(sub)
			abortNow, skip, reopen := inst.IdleDeadlineAbort(sub, pingAt, lastPong)
			if abortNow {
				finishAbortWS(conn, sub)
				return false
			}
			if skip {
				return true
			}
			if reopen {
				return true
			}
			finishAbortWS(conn, sub)
			return false
		}
	}
}

func writeOneWS(conn *websocket.Conn, inst *core.DeviceInstance, sub *core.WSSub, ev core.Event, t time.Duration) error {
	if inst != nil && inst.WSCloseMode(sub) == core.WSAbort {
		finishAbortWS(conn, sub)
		return errWSAbort
	}
	_ = conn.SetWriteDeadline(time.Now().Add(t))
	if err := conn.WriteJSON(ev); err != nil {
		abortWS(conn, inst, sub)
		return err
	}
	return nil
}

type wsAbortError struct{}

func (wsAbortError) Error() string { return "ws abort" }

var errWSAbort wsAbortError

func abortWS(conn *websocket.Conn, inst *core.DeviceInstance, sub *core.WSSub) {
	if inst != nil {
		inst.RequestClose(sub, core.WSAbort)
	}
	finishAbortWS(conn, sub)
}

func finishAbortWS(conn *websocket.Conn, sub *core.WSSub) {
	if sub != nil {
		ch := sub.Inbox()
		if ch != nil {
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						_ = conn.Close()
						return
					}
				default:
					_ = conn.Close()
					return
				}
			}
		}
	}
	_ = conn.Close()
}
