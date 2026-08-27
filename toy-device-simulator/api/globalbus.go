package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"toy-device-simulator/core"
)

// Phase 4d 跨设备全局事件总线：各 EventLog 经 SetMirror 镜像到此，
// 统一分配 global_seq。总线锁是叶子锁：Publish 在 EventLog 临界区内被调，
// 此处禁止回调任何设备/manager 锁；投递用非阻塞 send，慢订阅直接掐掉。

type globalEvent struct {
	GlobalSeq int
	Ev        core.Event
}

// MarshalJSON 复用 core.Event 的 wire 格式并附加 global_seq。
func (g globalEvent) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(g.Ev)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["global_seq"] = g.GlobalSeq
	return json.Marshal(m)
}

type globalSub struct {
	ch chan globalEvent
}

type globalBus struct {
	mu             sync.Mutex
	cap            int
	seq            int
	evictedThrough int
	ring           []globalEvent
	subs           map[*globalSub]struct{}
}

func newGlobalBus(capacity int) *globalBus {
	if capacity <= 0 {
		capacity = 10000
	}
	return &globalBus{cap: capacity, subs: map[*globalSub]struct{}{}}
}

func (b *globalBus) Publish(ev core.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	ge := globalEvent{GlobalSeq: b.seq, Ev: ev}
	b.ring = append(b.ring, ge)
	for len(b.ring) > b.cap {
		b.evictedThrough = b.ring[0].GlobalSeq
		b.ring = b.ring[1:]
	}
	var slow []*globalSub
	for sub := range b.subs {
		select {
		case sub.ch <- ge:
		default:
			slow = append(slow, sub)
		}
	}
	// 慢订阅 abort：inbox 满即摘除并关 channel，写端见 closed 收尾。
	for _, sub := range slow {
		delete(b.subs, sub)
		close(sub.ch)
	}
}

// Subscribe 回放与登记同一临界区，避免缝隙丢事件。
func (b *globalBus) Subscribe(after int) (backlog []globalEvent, sub *globalSub, expired bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if after < b.evictedThrough {
		return nil, nil, true
	}
	for _, ge := range b.ring {
		if ge.GlobalSeq > after {
			backlog = append(backlog, ge)
		}
	}
	sub = &globalSub{ch: make(chan globalEvent, 256)}
	b.subs[sub] = struct{}{}
	return backlog, sub, false
}

func (b *globalBus) Unsubscribe(sub *globalSub) {
	if sub == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[sub]; ok {
		delete(b.subs, sub)
		close(sub.ch)
	}
}

func (b *globalBus) snapshotMeta() (evictedThrough, newest int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.evictedThrough, b.seq
}

// handleWSGlobalEvents GET /ws/events/global?after_global_seq=N：回放 + live。
func (s *Server) handleWSGlobalEvents(w http.ResponseWriter, r *http.Request) {
	after := 0
	if raw := r.URL.Query().Get("after_global_seq"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "after_global_seq 非法")
			return
		}
		after = n
	}
	backlog, sub, expired := s.bus.Subscribe(after)
	if expired {
		evicted, newest := s.bus.snapshotMeta()
		writeJSON(w, http.StatusGone, map[string]any{
			"error":               "global_seq_expired",
			"evicted_through_seq": evicted,
			"newest_seq":          newest,
		})
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.bus.Unsubscribe(sub)
		return
	}
	t := drainTimeout(s.opts.Config)
	// 读循环只为感知客户端断开与回 pong。
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				s.bus.Unsubscribe(sub)
				return
			}
		}
	}()
	defer func() {
		s.bus.Unsubscribe(sub)
		_ = conn.Close()
	}()
	for _, ge := range backlog {
		_ = conn.SetWriteDeadline(time.Now().Add(t))
		if err := conn.WriteJSON(ge); err != nil {
			return
		}
	}
	ping := time.NewTicker(t / 2)
	defer ping.Stop()
	for {
		select {
		case ge, ok := <-sub.ch:
			if !ok {
				// 慢订阅被总线掐掉或已取消。
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(t))
			if err := conn.WriteJSON(ge); err != nil {
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(t)); err != nil {
				return
			}
		}
	}
}
