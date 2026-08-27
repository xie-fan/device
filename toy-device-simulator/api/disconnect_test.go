package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Phase 4e 断开即 interrupt：事件 WS 订阅 abort 后打断当前 turn（默认关）。

func disconnectBody(e *testEnv, id string, on bool) map[string]any {
	body := e.deviceBody(id)
	beh, _ := body["behavior"].(map[string]any)
	beh["first_reply_timeout_sec"] = 8
	if on {
		beh["interrupt_on_disconnect"] = true
	}
	return body
}

// dialEventsWS 连事件 WS 并读到至少一条，确保订阅进入 live。
func dialEventsWS(t *testing.T, e *testEnv, id, ins string) *websocket.Conn {
	t.Helper()
	u := wsURL(e.srv.URL, "/ws/events", "device_id="+id+"&instance_id="+ins)
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		_ = c.Close()
		t.Fatalf("读首条事件失败: %v", err)
	}
	return c
}

func TestInterruptOnDisconnectCancelsTurn(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", e.createBody(disconnectBody(e, "sim_iod", true)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_iod")
	e.waitReady(t, "sim_iod", ins, gen)
	assetID := e.uploadWAV(t)

	if code, raw := e.post(t, "/devices/sim_iod/speak", map[string]any{"asset_id": assetID}); code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d %s", code, raw)
	}
	c := dialEventsWS(t, e, "sim_iod", ins)
	// 客户端强断 → reader abort → interrupt 当前 turn。
	_ = c.Close()
	waitEvent(t, e, "sim_iod", ins, 4*time.Second,
		"turn_terminal", `"turn_end_reason":"interrupt"`)
}

func TestNoInterruptOnDisconnectWhenDisabled(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", e.createBody(disconnectBody(e, "sim_iod0", false)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_iod0")
	e.waitReady(t, "sim_iod0", ins, gen)
	assetID := e.uploadWAV(t)

	if code, raw := e.post(t, "/devices/sim_iod0/speak", map[string]any{"asset_id": assetID}); code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d %s", code, raw)
	}
	c := dialEventsWS(t, e, "sim_iod0", ins)
	_ = c.Close()
	time.Sleep(500 * time.Millisecond)
	_, evRaw, _ := e.get(t, "/devices/sim_iod0/events?instance_id="+ins)
	if containsBytes(evRaw, "turn_terminal") {
		t.Fatalf("默认关闭：断开不得打断 turn: %s", evRaw)
	}
}
