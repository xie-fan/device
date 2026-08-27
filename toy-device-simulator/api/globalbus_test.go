package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"toy-device-simulator/manager"
)

// Phase 4d 全局事件总线：global_seq 回放 + live，跨设备汇聚。

func readGlobalWS(t *testing.T, c *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("读全局 WS 失败: %v", err)
	}
	return m
}

func TestGlobalEventBusReplayAndLive(t *testing.T) {
	e := newEnv(t)
	insA, genA := e.createStartReady(t, "sim_ga")
	_ = genA

	u := wsURL(e.srv.URL, "/ws/events/global", "after_global_seq=0")
	c, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v resp=%v", err, resp)
	}
	defer c.Close()

	// 回放应包含设备 A 的 ready 事件，条目带 global_seq/device_id/instance_id。
	lastSeq := 0
	foundReady := false
	for i := 0; i < 50 && !foundReady; i++ {
		m := readGlobalWS(t, c, 2*time.Second)
		gs := intField(m, "global_seq")
		if gs <= lastSeq {
			t.Fatalf("global_seq 应严格递增: %d -> %d (%v)", lastSeq, gs, m)
		}
		lastSeq = gs
		if strField(m, "device_id") == "sim_ga" && strField(m, "instance_id") == insA &&
			strField(m, "event_type") == "ready" {
			foundReady = true
		}
	}
	if !foundReady {
		t.Fatal("回放应含设备 A 的 ready 事件")
	}

	// live：另一台设备的事件也应到达同一订阅。
	insB, genB := e.createStartReady(t, "sim_gb")
	_ = insB
	_ = genB
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("live 阶段应收到设备 B 的事件")
		}
		m := readGlobalWS(t, c, 2*time.Second)
		gs := intField(m, "global_seq")
		if gs <= lastSeq {
			t.Fatalf("global_seq 应严格递增: %d -> %d", lastSeq, gs)
		}
		lastSeq = gs
		if strField(m, "device_id") == "sim_gb" && strField(m, "event_type") == "ready" {
			return
		}
	}
}

func TestGlobalEventBusStaleCursor410(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.EventLogMaxEntries = 1 })
	e.createStartReady(t, "sim_g410") // 产生多条事件 → 总线容量 1，evictedThrough > 0

	u := wsURL(e.srv.URL, "/ws/events/global", "after_global_seq=0")
	c, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if c != nil {
		_ = c.Close()
		t.Fatal("过期游标不得升级")
	}
	if resp == nil {
		t.Fatalf("应返回 HTTP 响应，err=%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("after < evicted_through 应 410，得到 %d", resp.StatusCode)
	}
}

func TestGlobalEventBusBadAfter400(t *testing.T) {
	e := newEnv(t)
	u := wsURL(e.srv.URL, "/ws/events/global", "after_global_seq=abc")
	c, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if c != nil {
		_ = c.Close()
		t.Fatal("非法参数不得升级")
	}
	if resp == nil {
		t.Fatalf("应返回 HTTP 响应，err=%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("after_global_seq 非法应 400，得到 %d", resp.StatusCode)
	}
}
