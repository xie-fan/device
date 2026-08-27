package api

import (
	"net/http"
	"testing"
	"time"
)

// Phase 4 speak backlog：排队、出队、满、收口清队。

func backlogBody(e *testEnv, id string, depth int) map[string]any {
	body := e.deviceBody(id)
	beh, _ := body["behavior"].(map[string]any)
	beh["speak_backlog_depth"] = depth
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	return body
}

func TestSpeakBacklogQueuesThenAutoDispatches(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	code, raw := e.post(t, "/devices", e.createBody(backlogBody(e, "sim_bkl", 2)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_bkl")
	e.waitReady(t, "sim_bkl", ins, gen)
	assetID := e.uploadWAV(t)

	code, raw = e.post(t, "/devices/sim_bkl/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("第一个 speak 应 202，得到 %d %s", code, raw)
	}
	first := decodeMap(t, raw)
	if _, has := first["queued"]; has {
		t.Fatalf("槽空闲不应排队: %s", raw)
	}

	// 第二个进队列并在第一个终态后自动出队；speak_and_wait 直接等到它的终态。
	code, raw = e.post(t, "/devices/sim_bkl/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 10,
	})
	if code != http.StatusOK {
		t.Fatalf("排队项 speak_and_wait 应 200，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if strField(m, "event_type") != "turn_terminal" {
		t.Fatalf("排队项应以 turn_terminal 收梢: %s", raw)
	}

	// 事件带应有 speak_queued 与 speak_dequeued。
	code, evRaw, _ := e.get(t, "/devices/sim_bkl/events?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("GET events %d %s", code, evRaw)
	}
	for _, want := range []string{"speak_queued", "speak_dequeued"} {
		if !containsBytes(evRaw, want) {
			t.Fatalf("事件带应含 %s: %s", want, evRaw)
		}
	}
}

func TestSpeakBacklogFull409(t *testing.T) {
	e := newEnv(t)
	// 不回 TTS：第一个 turn 等 first_reply 超时前一直占槽。
	code, raw := e.post(t, "/devices", e.createBody(backlogBody(e, "sim_bklf", 1)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_bklf")
	e.waitReady(t, "sim_bklf", ins, gen)
	assetID := e.uploadWAV(t)

	if code, raw := e.post(t, "/devices/sim_bklf/speak", map[string]any{"asset_id": assetID}); code != http.StatusAccepted {
		t.Fatalf("第一个 speak 应 202，得到 %d %s", code, raw)
	}
	code, raw = e.post(t, "/devices/sim_bklf/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("第二个应排队 202，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if b, _ := m["queued"].(bool); !b || intField(m, "queue_position") != 1 {
		t.Fatalf("第二个应 queued pos=1: %s", raw)
	}
	code, raw = e.post(t, "/devices/sim_bklf/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusConflict || !containsBytes(raw, "speak_backlog_full") {
		t.Fatalf("队满应 409 speak_backlog_full，得到 %d %s", code, raw)
	}
	// GET /devices/{id} 应报队列长度。
	gcode, graw, _ := e.get(t, "/devices/sim_bklf")
	if gcode != http.StatusOK {
		t.Fatalf("GET device %d %s", gcode, graw)
	}
	if intField(decodeMap(t, graw), "speak_backlog_len") != 1 {
		t.Fatalf("speak_backlog_len 应为 1: %s", graw)
	}
}

func TestSpeakBacklogDepthZeroKeeps409(t *testing.T) {
	e := newEnv(t)
	body := e.deviceBody("sim_bkl0")
	beh, _ := body["behavior"].(map[string]any)
	beh["first_reply_timeout_sec"] = 5
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_bkl0")
	e.waitReady(t, "sim_bkl0", ins, gen)
	assetID := e.uploadWAV(t)
	if code, raw := e.post(t, "/devices/sim_bkl0/speak", map[string]any{"asset_id": assetID}); code != http.StatusAccepted {
		t.Fatalf("第一个 speak 应 202，得到 %d %s", code, raw)
	}
	code, raw = e.post(t, "/devices/sim_bkl0/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusConflict {
		t.Fatalf("depth=0 槽占用仍应 409，得到 %d %s", code, raw)
	}
	if containsBytes(raw, "speak_backlog") {
		t.Fatalf("关闭时错误不应提 backlog: %s", raw)
	}
}

func TestSpeakBacklogClearedOnStopAndWaiterWoken(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", e.createBody(backlogBody(e, "sim_bkls", 2)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_bkls")
	e.waitReady(t, "sim_bkls", ins, gen)
	assetID := e.uploadWAV(t)
	if code, raw := e.post(t, "/devices/sim_bkls/speak", map[string]any{"asset_id": assetID}); code != http.StatusAccepted {
		t.Fatalf("第一个 speak 应 202，得到 %d %s", code, raw)
	}
	// 排队项挂 speak_and_wait，stop 后应被 dropped 事件唤醒而非等到超时。
	done := make(chan [2]any, 1)
	go func() {
		c, b := e.post(t, "/devices/sim_bkls/speak_and_wait", map[string]any{
			"asset_id": assetID, "timeout_sec": 8,
		})
		done <- [2]any{c, b}
	}()
	time.Sleep(150 * time.Millisecond)
	if code, raw := e.post(t, "/devices/sim_bkls/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, raw)
	}
	select {
	case got := <-done:
		c := got[0].(int)
		b := got[1].([]byte)
		if c != http.StatusOK || !containsBytes(b, "speak_backlog_dropped") {
			t.Fatalf("排队 waiter 应被 dropped 唤醒（200 + event_type），得到 %d %s", c, b)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("stop 后排队 waiter 应立即被唤醒")
	}
	code, evRaw, _ := e.get(t, "/devices/sim_bkls/events?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("GET events %d %s", code, evRaw)
	}
	if !containsBytes(evRaw, "speak_backlog_dropped") {
		t.Fatalf("事件带应含 speak_backlog_dropped: %s", evRaw)
	}
	gcode, graw, _ := e.get(t, "/devices/sim_bkls")
	if intField(decodeMap(t, graw), "speak_backlog_len") != 0 {
		t.Fatalf("stop 后 speak_backlog_len 应为 0（%d）: %s", gcode, graw)
	}
}
