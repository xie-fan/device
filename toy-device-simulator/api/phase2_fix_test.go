package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"toy-device-simulator/protocol"
)

func TestJSONAckCreateDeviceAndOutbound(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	e.auto.needAck = true
	body := e.deviceBody("sim_jsonack")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_ack"] = map[string]any{"mode": "json", "sleep_ms": 500, "code": 1}
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("Phase 2 创建设备应允许 json ACK，得到 %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_jsonack")
	e.waitReady(t, "sim_jsonack", ins, gen)
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_jsonack/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d %s", code, raw)
	}
	deadline := time.Now().Add(4 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		c := e.conn("sim_jsonack")
		if c != nil {
			for _, w := range c.Writes() {
				if len(w) == 0 {
					continue
				}
				if w[0] == protocol.FirstAck {
					t.Fatal("mode=json 不应出站 binary '4'")
				}
				if w[0] == protocol.FirstManage {
					env, err := protocol.DecodeManage(w)
					if err == nil && containsBytes([]byte(env.Topic), "downlink-ack/server") {
						found = true
					}
				}
			}
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatal("mode=json 出站 ACK 首字节应为 '1' 且 topic 含 downlink-ack/server")
	}
}

func TestPhase2RecordingThreeLevelPaths(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := e.deviceBody("sim_rec3")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_rec3")
	e.waitReady(t, "sim_rec3", ins, gen)
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_rec3/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak %d %s", code, raw)
	}
	turnID := strField(decodeMap(t, raw), "turn_id")
	if turnID == "" {
		t.Fatal("turn_id 空")
	}
	q := "?instance_id=" + ins
	deadline := time.Now().Add(5 * time.Second)
	var framesCode, downCode int
	var framesBody []byte
	for time.Now().Before(deadline) {
		framesCode, framesBody, _ = e.get(t, "/devices/sim_rec3/turns/"+turnID+"/frames"+q)
		downCode, _, _ = e.get(t, "/devices/sim_rec3/turns/"+turnID+"/audio/downlink"+q)
		if framesCode == http.StatusOK && downCode == http.StatusOK {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if framesCode != http.StatusOK {
		t.Fatalf("GET frames 三级路径应 200，得到 %d %s", framesCode, framesBody)
	}
	if downCode != http.StatusOK {
		t.Fatalf("GET downlink 三级路径应 200，得到 %d", downCode)
	}
	twoLevel := filepath.Join(e.recDir, "sim_rec3", turnID, "frames.jsonl")
	if _, err := os.Stat(twoLevel); err == nil {
		t.Fatal("Phase 2 成功不得依赖两级旧路径")
	}
}

func TestWaitReadyPhaseCWakes409Not504(t *testing.T) {
	e := newEnv(t)
	e.auto.silent = true
	e.createDevice(t, "sim_pc")
	ins, gen := e.startDevice(t, "sim_pc")
	done := make(chan [2]any, 1)
	go func() {
		c, b := e.post(t, "/devices/sim_pc/wait_ready", map[string]any{
			"instance_id": ins, "conn_generation": gen, "timeout_sec": 5,
		})
		done <- [2]any{c, b}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c := e.conn("sim_pc"); c != nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case got := <-done:
		c := got[0].(int)
		b := got[1].([]byte)
		if c != http.StatusConflict || !containsBytes(b, "generation_gone") {
			t.Fatalf("Phase C committed 后挂起 waiter 应 409 不是 504，得到 %d %s", c, b)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("wait_ready 应被 409 唤醒")
	}
}

func TestWaitLiveEventAfterRegister200Not504(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := e.deviceBody("sim_wlive")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_wlive")
	e.waitReady(t, "sim_wlive", ins, gen)
	gcode, gbody, _ := e.get(t, "/devices/sim_wlive/events?instance_id="+ins)
	if gcode != http.StatusOK {
		t.Fatalf("events %d %s", gcode, gbody)
	}
	newest := 0
	var emap map[string]any
	_ = json.Unmarshal(gbody, &emap)
	if evs, ok := emap["events"].([]any); ok {
		for _, row := range evs {
			rm, _ := row.(map[string]any)
			if n := intField(rm, "event_seq"); n > newest {
				newest = n
			}
		}
	}
	assetID := e.uploadWAV(t)
	done := make(chan [2]any, 1)
	go func() {
		c, b := e.post(t, "/wait", map[string]any{
			"device_id":       "sim_wlive",
			"instance_id":     ins,
			"event_type":      "tts_done",
			"after_event_seq": newest,
			"conn_generation": gen,
			"timeout_sec":     5,
		})
		done <- [2]any{c, b}
	}()
	time.Sleep(40 * time.Millisecond)
	code, raw = e.post(t, "/devices/sim_wlive/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak %d %s", code, raw)
	}
	select {
	case got := <-done:
		c := got[0].(int)
		b := got[1].([]byte)
		if c != http.StatusOK {
			t.Fatalf("事件已进日志后 /wait 应 200 禁止 504，得到 %d %s", c, b)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("/wait 应 200")
	}
}

func TestLiveWSReceivesSubsequentTurnTerminalAndDeviceDeleted(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := e.deviceBody("sim_wslive")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_wslive")
	e.waitReady(t, "sim_wslive", ins, gen)
	q := "device_id=sim_wslive&instance_id=" + ins
	u := wsURL(e.srv.URL, "/ws/events", q)
	c, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		st := 0
		if resp != nil {
			st = resp.StatusCode
			resp.Body.Close()
		}
		t.Fatalf("live WS 应升级 err=%v status=%d", err, st)
	}
	defer c.Close()
	sawReady := false
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !sawReady {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("读 backlog ready 失败: %v", err)
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if strField(ev, "event_type") == "ready" {
			sawReady = true
		}
	}
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_wslive/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak %d %s", code, raw)
	}
	sawTerminal := false
	_ = c.SetReadDeadline(time.Now().Add(6 * time.Second))
	for !sawTerminal {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("live WS 等待 turn_terminal 失败: %v", err)
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if strField(ev, "event_type") == "turn_terminal" {
			sawTerminal = true
		}
	}
	code, _ = e.del(t, "/devices/sim_wslive")
	if code != http.StatusOK {
		t.Fatalf("DELETE %d", code)
	}
	sawDeleted := false
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !sawDeleted {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("live WS 等待 device_deleted 失败: %v", err)
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if strField(ev, "event_type") == "device_deleted" {
			sawDeleted = true
		}
	}
}

func TestDisconnectAfterRunningReleasesPermitAndAllowsRestart(t *testing.T) {
	e := newEnv(t)
	ins, gen := e.createStartReady(t, "sim_drop")
	_ = ins
	_ = gen
	c := e.conn("sim_drop")
	if c == nil {
		t.Fatal("应有 fakeConn")
	}
	_ = c.Close()
	deadline := time.Now().Add(3 * time.Second)
	stopped := false
	for time.Now().Before(deadline) {
		code, body, _ := e.get(t, "/devices/sim_drop")
		if code == http.StatusOK {
			m := decodeMap(t, body)
			if strField(m, "instance_state") == "stopped" {
				stopped = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !stopped {
		t.Fatal("连接异常收口后 Manager 应标 Stopped 并释放 permit")
	}
	code, body := e.post(t, "/devices/sim_drop/start", nil)
	if code != http.StatusAccepted {
		t.Fatalf("permit 释放后再次 start 应 202 不是 409，得到 %d %s", code, body)
	}
}

func TestScenarioExecutesBatchStartSpeakAssert(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	for _, id := range []string{"sim_sc1", "sim_sc2"} {
		body := e.deviceBody(id)
		beh, _ := body["behavior"].(map[string]any)
		beh["downlink_idle_timeout_sec"] = 1
		beh["first_reply_timeout_sec"] = 2
		code, raw := e.post(t, "/devices", e.createBody(body))
		if code != http.StatusCreated {
			t.Fatalf("create %s %d %s", id, code, raw)
		}
	}
	assetID := e.uploadWAV(t)
	code, raw := e.post(t, "/scenarios/run", map[string]any{
		"name": "batch-hello",
		"steps": []any{
			map[string]any{"action": "batch_start", "device_ids": []string{"sim_sc1", "sim_sc2"}, "stagger_ms": 10},
			map[string]any{"action": "speak", "device_id": "sim_sc1", "asset_id": assetID, "wait": true},
			map[string]any{
				"action":          "assert",
				"device_id":       "sim_sc1",
				"instance_id":     "$prev.instance_id",
				"event_type":      "tts_done",
				"turn_id":         "$prev.turn_id",
				"after_event_seq": "$prev.seq_before",
			},
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("run 应 202，得到 %d %s", code, raw)
	}
	runID := strField(decodeMap(t, raw), "run_id")
	deadline := time.Now().Add(12 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		gcode, gbody, _ := e.get(t, "/scenarios/runs/"+runID)
		if gcode != http.StatusOK {
			t.Fatalf("GET run %d %s", gcode, gbody)
		}
		last = decodeMap(t, gbody)
		st := strField(last, "status")
		if st == "succeeded" {
			return
		}
		if st == "failed" {
			t.Fatalf("scenario 失败: %s", gbody)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("scenario 应结束且 succeeded，最后 %v", last)
}

func TestScenarioFailedStepVisibleInGET(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_sf")
	code, raw := e.post(t, "/scenarios/run", map[string]any{
		"name": "fail-assert",
		"steps": []any{
			map[string]any{
				"action":          "assert",
				"device_id":       "sim_sf",
				"instance_id":     "ins_missing",
				"event_type":      "tts_done",
				"after_event_seq": 0,
			},
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("run 应 202，得到 %d %s", code, raw)
	}
	runID := strField(decodeMap(t, raw), "run_id")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		gcode, gbody, _ := e.get(t, "/scenarios/runs/"+runID)
		if gcode != http.StatusOK {
			t.Fatalf("GET %d %s", gcode, gbody)
		}
		m := decodeMap(t, gbody)
		if strField(m, "status") == "failed" {
			steps, _ := m["steps"].([]any)
			if len(steps) == 0 {
				t.Fatal("失败步骤应出现在 GET steps 里")
			}
			row, _ := steps[0].(map[string]any)
			if strField(row, "status") != "failed" {
				t.Fatalf("步骤 status 应为 failed，得到 %s", gbody)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("失败步骤要反映在 GET 状态里")
}

func TestWaitSpeakableRequiresConnGeneration(t *testing.T) {
	e := newEnv(t)
	ins, gen := e.createStartReady(t, "sim_nogen")
	code, body := e.post(t, "/wait", map[string]any{
		"device_id":   "sim_nogen",
		"instance_id": ins,
		"event_type":  "connected",
		"timeout_sec": 2,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("Ready 设备 POST /wait 无 conn_generation 应 400，得到 %d %s", code, body)
	}
	code, body = e.post(t, "/wait", map[string]any{
		"device_id":       "sim_nogen",
		"instance_id":     ins,
		"event_type":      "connected",
		"conn_generation": gen,
		"timeout_sec":     2,
	})
	if code != http.StatusOK {
		t.Fatalf("带 conn_generation 等 connected 应 200，得到 %d %s", code, body)
	}
}

func TestWaitPrevGenerationNotCompletedByNewTurnTerminal(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	ins, gen := e.createStartReady(t, "sim_wcross")
	done := make(chan [2]any, 1)
	go func() {
		c, b := e.post(t, "/wait", map[string]any{
			"device_id":       "sim_wcross",
			"instance_id":     ins,
			"event_type":      "turn_terminal",
			"conn_generation": gen,
			"timeout_sec":     3,
		})
		done <- [2]any{c, b}
	}()
	time.Sleep(80 * time.Millisecond)
	code, body := e.post(t, "/devices/sim_wcross/stop", nil)
	if code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, body)
	}
	ins2, gen2 := e.startDevice(t, "sim_wcross")
	if ins2 != ins {
		t.Fatalf("stop/start 应保持 instance_id，%s vs %s", ins, ins2)
	}
	if gen2 <= gen {
		t.Fatalf("新一代 conn_generation 应递增，%d -> %d", gen, gen2)
	}
	e.waitReady(t, "sim_wcross", ins2, gen2)
	assetID := e.uploadWAV(t)
	code, body = e.post(t, "/devices/sim_wcross/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d %s", code, body)
	}
	select {
	case got := <-done:
		c := got[0].(int)
		b := got[1].([]byte)
		if c == http.StatusOK {
			t.Fatalf("上一代 waiter 不得被新一代 turn_terminal 以 200 完成，body=%s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("上一代 waiter 应结束（409/超时），不得一直挂起")
	}
}
