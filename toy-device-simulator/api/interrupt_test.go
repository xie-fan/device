package api

import (
	"net/http"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

func TestInterruptIdle200InterruptedFalse(t *testing.T) {
	e := newEnv(t)
	ins, _ := e.createStartReady(t, "sim_idle")
	code, body := e.post(t, "/devices/sim_idle/interrupt", map[string]any{"instance_id": ins})
	if code != http.StatusOK {
		t.Fatalf("无活动 Turn interrupt 应 200，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if boolField(m, "interrupted") {
		t.Fatalf("槽空应 interrupted=false，body=%s", body)
	}
}

func TestInterruptIdleWithTurnIDStill200(t *testing.T) {
	e := newEnv(t)
	ins, _ := e.createStartReady(t, "sim_idle_tid")
	code, body := e.post(t, "/devices/sim_idle_tid/interrupt", map[string]any{
		"instance_id": ins, "turn_id": "turn_not_there",
	})
	if code != http.StatusOK {
		t.Fatalf("槽空即使带 turn_id 也应 200 不是 409，得到 %d body=%s", code, body)
	}
	if boolField(decodeMap(t, body), "interrupted") {
		t.Fatalf("槽空应 interrupted=false，body=%s", body)
	}
}

func TestInterruptActiveTurn200DoesNotBeginClose(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = false
	ins, _ := e.createStartReady(t, "sim_int")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_int/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	code, body = e.post(t, "/devices/sim_int/interrupt", map[string]any{"instance_id": ins})
	if code != http.StatusOK {
		t.Fatalf("活动 Turn interrupt 应 200，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if !boolField(m, "interrupted") {
		t.Fatalf("应 interrupted=true，body=%s", body)
	}
	if strField(m, "turn_id") != turnID {
		t.Fatalf("应带回 turn_id，body=%s", body)
	}
	if strField(m, "turn_end_reason") != "interrupt" {
		t.Fatalf("turn_end_reason 应为 interrupt，body=%s", body)
	}
	c := e.conn("sim_int")
	if c == nil {
		t.Fatal("应有 FakeConn")
	}
	if c.CloseCount() != 0 {
		t.Fatal("interrupt 不得 BeginClose，连接应保持")
	}
}

func TestInterruptWrongInstance409GenerationGone(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_wi")
	code, body := e.post(t, "/devices/sim_wi/interrupt", map[string]any{"instance_id": "ins_other"})
	if code != http.StatusConflict || !containsBytes(body, "generation_gone") {
		t.Fatalf("instance_id 非当前 live 应 409 generation_gone，得到 %d body=%s", code, body)
	}
}

func TestInterruptTurnMismatch409DoesNotCancelTurn(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = false
	ins, _ := e.createStartReady(t, "sim_tm")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_tm/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_tm/interrupt", map[string]any{
		"instance_id": ins, "turn_id": "trn_not_current",
	})
	if code != http.StatusConflict || !containsBytes(body, "turn_mismatch") {
		t.Fatalf("turn_id 不是当前槽应 409 turn_mismatch，得到 %d body=%s", code, body)
	}
	c := e.conn("sim_tm")
	if c != nil && c.HasStage(protocol.StageBreak) {
		t.Fatal("turn_mismatch 不得 CancelTurn / 发 Stage=3")
	}
	code2, body2 := e.post(t, "/devices/sim_tm/speak", map[string]any{"asset_id": assetID})
	if code2 != http.StatusConflict {
		t.Fatalf("未取消则槽仍占用，再 speak 应 409，得到 %d body=%s", code2, body2)
	}
}

func TestInterruptFinalizeStarted409(t *testing.T) {
	e := newEnv(t)
	e.auto.hold = true
	e.auto.replyTTS = false
	ins, _ := e.createStartReady(t, "sim_fs")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_fs/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	done := make(chan [2]any, 1)
	go func() {
		c, b := e.post(t, "/devices/sim_fs/stop", nil)
		done <- [2]any{c, b}
	}()
	time.Sleep(80 * time.Millisecond)
	code, body = e.post(t, "/devices/sim_fs/interrupt", map[string]any{"instance_id": ins})
	if code != http.StatusConflict || !containsBytes(body, "finalize_started") {
		t.Fatalf("finalize_started 时 interrupt 应 409 且不 CancelTurn，得到 %d body=%s", code, body)
	}
	if c := e.conn("sim_fs"); c != nil {
		c.ReleaseWriteGate()
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop 未返回")
	}
}

func TestPUTDeviceIDOrWriteQueue400(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_put")
	code, body := e.put(t, "/devices/sim_put/config", map[string]any{"device_id": "other"})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT device_id 应 400，得到 %d body=%s", code, body)
	}
	code, body = e.put(t, "/devices/sim_put/config", map[string]any{"write_queue_depth": 256})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT write_queue_depth 应 400，得到 %d body=%s", code, body)
	}
	code, body = e.put(t, "/devices/sim_put/config", map[string]any{"write_drain_timeout_sec": 2})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT write_drain_timeout_sec 应 400，得到 %d body=%s", code, body)
	}
}

func TestPUTIdentityWhileRunning409(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_id")
	// Phase 11：挂靠归 start，PUT /config 碰它一律 400，不分运行状态。
	code, body := e.put(t, "/devices/sim_id/config", map[string]any{"enterprise": "other"})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT 挂靠字段应 400，得到 %d body=%s", code, body)
	}
	code, body = e.put(t, "/devices/sim_id/config", map[string]any{"playing_mode": 2})
	if code != http.StatusConflict {
		t.Fatalf("Running PUT playing_mode 应 409，热更只许 report，得到 %d body=%s", code, body)
	}
}

func TestPlayingModeHotUpdateOnlyViaReportWhenReady(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_pm")
	code, body := e.put(t, "/devices/sim_pm/config", map[string]any{"playing_mode": 2})
	if code != http.StatusConflict {
		t.Fatalf("Ready 时 PUT playing_mode 不得 200，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_pm/report", map[string]any{"playingMode": 2})
	if code != http.StatusAccepted {
		t.Fatalf("Ready 时 POST /report 才是 playing_mode 热更路径，应 202，得到 %d body=%s", code, body)
	}
	gcode, gbody, _ := e.get(t, "/devices/sim_pm")
	if gcode != http.StatusOK {
		t.Fatalf("GET device 应 200，得到 %d body=%s", gcode, gbody)
	}
	if intField(decodeMap(t, gbody), "playing_mode") != 2 {
		t.Fatalf("report 后 playing_mode 应为 2，body=%s", gbody)
	}
}

func TestFaultSkipRegisterSpeakableWhenRunningConnected(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_sr")
	code, body := e.post(t, "/devices/sim_sr/faults", map[string]any{"fault": "skip_register"})
	if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusAccepted {
		t.Fatalf("Created 上 POST faults 应成功，得到 %d body=%s", code, body)
	}
	ins, _ := e.startDevice(t, "sim_sr")
	time.Sleep(50 * time.Millisecond)
	c := e.conn("sim_sr")
	if c != nil {
		for _, w := range c.Writes() {
			if manageIsRegister(w) {
				t.Fatal("skip_register 不得发 register")
			}
		}
	}
	assetID := e.uploadWAV(t)
	code, body = e.post(t, "/devices/sim_sr/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("skip_register 时 Running+Connected 应可 speak（202 不是 409），得到 %d body=%s", code, body)
	}
	_ = ins
}

func TestFailedJSONCancelTurnKeepsConnection(t *testing.T) {
	e := newEnv(t)
	e.auto.failJSON = true
	e.auto.replyTTS = false
	ins, _ := e.createStartReady(t, "sim_fj")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_fj/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 5,
	})
	if code != http.StatusOK {
		t.Fatalf("失败 JSON 应收口 Turn，speak_and_wait 应 200，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "turn_end_reason") != "error" {
		t.Fatalf("失败 JSON 的 turn_end_reason 应为 error，body=%s", body)
	}
	c := e.conn("sim_fj")
	if c == nil {
		t.Fatal("应有 FakeConn")
	}
	if c.CloseCount() != 0 {
		t.Fatal("失败 JSON 必须 CancelTurn，连接保持，不得 BeginClose")
	}
	if !c.HasStage(protocol.StageBreak) {
		t.Fatal("失败 JSON 应按 CancelTurn 写出 Stage=3")
	}
	gcode, gbody, _ := e.get(t, "/devices/sim_fj")
	if gcode != http.StatusOK {
		t.Fatalf("连接保持则设备仍 live，GET %d %s", gcode, gbody)
	}
	_ = ins
}
