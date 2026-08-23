package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"toy-device-simulator/manager"
)

func TestWaitOmittedAfterEventSeqEqualsZero(t *testing.T) {
	e := newEnv(t)
	ins, _ := e.createStartReady(t, "sim_om")
	code, body := e.post(t, "/wait", map[string]any{
		"device_id":   "sim_om",
		"instance_id": ins,
		"event_type":  "connected",
		"timeout_sec": 2,
	})
	if code != http.StatusOK {
		t.Fatalf("省略 after_event_seq ≡ 0，历史 connected 应 200 而非空等，得到 %d body=%s", code, body)
	}
	if intField(decodeMap(t, body), "event_seq") < 1 {
		t.Fatalf("应从 oldest 起命中 seq>=1，body=%s", body)
	}
}

func TestWaitLiveHistoryHit200Not504(t *testing.T) {
	e := newEnv(t)
	ins, _ := e.createStartReady(t, "sim_hist")
	code, body := e.post(t, "/wait", map[string]any{
		"device_id":       "sim_hist",
		"instance_id":     ins,
		"event_type":      "ready",
		"after_event_seq": 0,
		"timeout_sec":     1,
	})
	if code != http.StatusOK {
		t.Fatalf("live 历史已命中应 200 不是 504，得到 %d body=%s", code, body)
	}
}

func TestWaitTombstoneMiss404Not504(t *testing.T) {
	e := newEnv(t)
	ins := e.createDevice(t, "sim_tm")
	code, body := e.del(t, "/devices/sim_tm")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/wait", map[string]any{
		"device_id":       "sim_tm",
		"instance_id":     ins,
		"event_type":      "tts_done",
		"after_event_seq": 0,
		"timeout_sec":     1,
	})
	if code != http.StatusNotFound {
		t.Fatalf("tombstone 历史未命中应 404 不是 504，得到 %d body=%s", code, body)
	}
}

func TestWSEventsMissingIDs400NoUpgrade(t *testing.T) {
	e := newEnv(t)
	u := wsURL(e.srv.URL, "/ws/events", "")
	c, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if c != nil {
		_ = c.Close()
		t.Fatal("缺 device_id/instance_id 不得升级")
	}
	if resp == nil {
		t.Fatalf("应返回 HTTP 响应，err=%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 ID 应 400 不升级，得到 %d err=%v", resp.StatusCode, err)
	}
}

func TestWSEventsStaleCursor410NoUpgrade(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.EventLogMaxEntries = 1 })
	ins, _ := e.createStartReady(t, "sim_410")
	q := "device_id=sim_410&instance_id=" + ins + "&after_event_seq=0"
	u := wsURL(e.srv.URL, "/ws/events", q)
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
		t.Fatalf("after < evicted_through_seq 应 410 不升级，得到 %d err=%v", resp.StatusCode, err)
	}
}

func TestWSTombstoneReplaysThroughDeviceDeletedThenClose(t *testing.T) {
	e := newEnv(t)
	ins := e.createDevice(t, "sim_wsd")
	code, body := e.del(t, "/devices/sim_wsd")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	q := "device_id=sim_wsd&instance_id=" + ins
	u := wsURL(e.srv.URL, "/ws/events", q)
	c, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		st := 0
		if resp != nil {
			st = resp.StatusCode
			resp.Body.Close()
		}
		t.Fatalf("tombstone WS 应升级回放，err=%v status=%d", err, st)
	}
	defer c.Close()
	sawDeleted := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, msg, err := c.ReadMessage()
		if err != nil {
			if !sawDeleted {
				t.Fatal("关闭前应回放到 device_deleted")
			}
			return
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if strField(ev, "event_type") == "device_deleted" {
			sawDeleted = true
		}
	}
	if !sawDeleted {
		t.Fatal("应收到 device_deleted")
	}
	t.Fatal("tombstone WS 回放到 device_deleted 后应关闭，不得空等")
}
