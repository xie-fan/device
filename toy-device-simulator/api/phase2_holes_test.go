package api

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
	"toy-device-simulator/manager"
	"toy-device-simulator/protocol"
)

func TestWaitEventTimeoutAfterRemoveFailsWaitsNotify(t *testing.T) {
	log := core.NewEventLog("sim_we", "ins_we")
	_, ch, expired, hit := log.FindOrRegisterWaiter(0, "ready", "")
	if expired || hit || ch == nil {
		t.Fatal("应登记 waiter")
	}
	ev, n := log.AppendLocked("ready", "", "", "", "", "")
	s := &Server{devices: map[string]*managedDevice{}, tombs: map[string]*tombstone{}}
	done := make(chan [2]any, 1)
	go func() {
		code, payload := s.waitEventTimeout("sim_we", "ins_we", log, nil, ch)
		done <- [2]any{code, payload}
	}()
	select {
	case got := <-done:
		t.Fatalf("摘表后、notify 前不得立刻返回（尤其不得空 200），得到 %v", got)
	case <-time.After(40 * time.Millisecond):
	}
	n.NotifyHTTP()
	select {
	case got := <-done:
		code := got[0].(int)
		payload, _ := got[1].(map[string]any)
		if code != http.StatusOK {
			t.Fatalf("通知到达后应 200，得到 %d %v", code, payload)
		}
		if strField(payload, "event_type") != "ready" {
			t.Fatalf("禁止字段全空的 200，payload=%v", payload)
		}
		seq := 0
		switch v := payload["event_seq"].(type) {
		case int:
			seq = v
		case float64:
			seq = int(v)
		}
		if seq != ev.EventSeq {
			t.Fatalf("禁止字段全空的 200，payload=%v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify 后应 200")
	}
}

func TestWaitReadyAfterSpeakableTakenWaitsNotifyNot504(t *testing.T) {
	inst := newHoleDevice(t, "sim_wrh")
	_, _, ch := inst.OfferSpeakableWait()
	if ch == nil {
		t.Fatal("未 speakable 应登记 waiter")
	}
	if !inst.RemoveSpeakableWaiter(ch) {
		t.Fatal("模拟 Phase C 摘表应成功")
	}
	s := &Server{}
	done := make(chan [2]any, 1)
	go func() {
		code, payload := s.waitReadyAfterTimeout("sim_wrh", inst.InstanceID(), 1, inst, ch)
		done <- [2]any{code, payload}
	}()
	select {
	case got := <-done:
		t.Fatalf("摘表后、notify 前不得 504，得到 %v", got)
	case <-time.After(40 * time.Millisecond):
	}
	ch <- core.SpeakableResult{Code: 200, State: core.ConnReady}
	select {
	case got := <-done:
		code := got[0].(int)
		payload, _ := got[1].(map[string]any)
		if code != http.StatusOK {
			t.Fatalf("通知 200 后应 200 不是 504，得到 %d %v", code, payload)
		}
		if strField(payload, "connection_state") != core.ConnReady.String() {
			t.Fatalf("应按通知填 connection_state，payload=%v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify 后应 200")
	}
}

func TestSpeakPermitReleasedOnTurnTerminal(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxConcurrentSpeaking = 1 })
	e.auto.replyTTS = true
	body := e.deviceBody("sim_sp2")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", map[string]any{"device": body})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_sp2")
	e.waitReady(t, "sim_sp2", ins, gen)
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_sp2/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("第一次 speak_and_wait 应 200，得到 %d %s", code, raw)
	}
	code, raw = e.post(t, "/devices/sim_sp2/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("Turn 终态应释放 speak_permit，再次 speak 应 202 不是 429，得到 %d %s", code, raw)
	}
}

func TestPUTAllowlistWritesOnCreated(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_putw")
	code, body := e.put(t, "/devices/sim_putw/config", map[string]any{
		"enterprise":       "acme",
		"device_type":      "A3",
		"playing_mode":     2,
		"action":           "chatbot",
		"firmware_version": "9.9.9",
		"nic_type":         "4g",
		"nic_iccid":        "89860000",
		"audio":            map[string]any{"slice_ms": 50},
		"server":           map[string]any{"url": "ws://127.0.0.1:9/"},
		"uuid":             map[string]any{"min": 10, "max": 20},
		"downlink_ack":     map[string]any{"mode": "json", "sleep_ms": 500, "code": 1},
		"behavior":         map[string]any{"keepalive_interval_sec": 15, "first_reply_timeout_sec": 7},
		"recording":        map[string]any{"enable_frame_log": false, "output_dir": e.recDir},
	})
	if code != http.StatusOK {
		t.Fatalf("Created PUT allowlist 应 200 并写入，得到 %d %s", code, body)
	}
	gcode, gbody, _ := e.get(t, "/devices/sim_putw/config")
	if gcode != http.StatusOK {
		t.Fatalf("GET config %d %s", gcode, gbody)
	}
	m := decodeMap(t, gbody)
	if strField(m, "enterprise") != "acme" || intField(m, "playing_mode") != 2 {
		t.Fatalf("enterprise/playing_mode 未写入: %s", gbody)
	}
	if strField(m, "firmware_version") != "9.9.9" || strField(m, "nic_type") != "4g" {
		t.Fatalf("firmware/nic 未写入: %s", gbody)
	}
	audio, _ := m["audio"].(map[string]any)
	if intField(audio, "slice_ms") != 50 {
		t.Fatalf("audio.slice_ms 未写入: %s", gbody)
	}
	server, _ := m["server"].(map[string]any)
	if strField(server, "url") != "ws://127.0.0.1:9/" {
		t.Fatalf("server.url 未写入: %s", gbody)
	}
	uuid, _ := m["uuid"].(map[string]any)
	if intField(uuid, "min") != 10 || intField(uuid, "max") != 20 {
		t.Fatalf("uuid 未写入: %s", gbody)
	}
	beh, _ := m["behavior"].(map[string]any)
	if intField(beh, "keepalive_interval_sec") != 15 || intField(beh, "first_reply_timeout_sec") != 7 {
		t.Fatalf("behavior 未写入: %s", gbody)
	}
	ack, _ := beh["downlink_ack"].(map[string]any)
	if strField(ack, "mode") != "json" || intField(ack, "sleep_ms") != 500 {
		t.Fatalf("downlink_ack 未写入: %s", gbody)
	}
	rec, _ := m["recording"].(map[string]any)
	if boolField(rec, "enable_frame_log") {
		t.Fatalf("recording.enable_frame_log 应为 false: %s", gbody)
	}
}

func TestPUTNestedWriteQueue400(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_pq")
	code, body := e.put(t, "/devices/sim_pq/config", map[string]any{
		"behavior": map[string]any{"write_queue_depth": 256},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("嵌套 write_queue_depth 应 400，得到 %d %s", code, body)
	}
	code, body = e.put(t, "/devices/sim_pq/config", map[string]any{
		"recording": map[string]any{"write_drain_timeout_sec": 2},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("任意嵌套 write_drain_timeout_sec 应 400，得到 %d %s", code, body)
	}
}

func TestPUTUnknownField400(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_unk")
	code, body := e.put(t, "/devices/sim_unk/config", map[string]any{"not_allowlisted": 1})
	if code != http.StatusBadRequest {
		t.Fatalf("未知非 allowlist 字段应 400，得到 %d %s", code, body)
	}
}

func TestPUTRecordingWhileRunning200(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_recr")
	out := filepath.Join(e.recDir, "alt")
	code, body := e.put(t, "/devices/sim_recr/config", map[string]any{
		"recording": map[string]any{"output_dir": out},
	})
	if code != http.StatusOK {
		t.Fatalf("Running 时 recording.* 应 200，得到 %d %s", code, body)
	}
	gcode, gbody, _ := e.get(t, "/devices/sim_recr/config")
	if gcode != http.StatusOK {
		t.Fatalf("GET %d %s", gcode, gbody)
	}
	rec, _ := decodeMap(t, gbody)["recording"].(map[string]any)
	if strField(rec, "output_dir") != out {
		t.Fatalf("recording.output_dir 未写入: %s", gbody)
	}
}

func TestUplinkGETMatchesSentNotDouble(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := e.deviceBody("sim_up1")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", map[string]any{"device": body})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_up1")
	e.waitReady(t, "sim_up1", ins, gen)
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_up1/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait %d %s", code, raw)
	}
	turnID := strField(decodeMap(t, raw), "turn_id")
	q := "?instance_id=" + ins
	var pcm []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		gcode, gbody, _ := e.get(t, "/devices/sim_up1/turns/"+turnID+"/audio/uplink"+q)
		if gcode == http.StatusOK {
			decoded, err := core.DecodeWAV(gbody)
			if err != nil {
				t.Fatalf("GET uplink 应是 WAV: %v", err)
			}
			pcm = decoded.Samples
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pcm) == 0 {
		t.Fatal("GET uplink 应有 PCM")
	}
	sent := 0
	c := e.conn("sim_up1")
	if c == nil {
		t.Fatal("应有 FakeConn")
	}
	for _, w := range c.Writes() {
		if len(w) == 0 || w[0] != protocol.FirstAudio {
			continue
		}
		view := protocol.Inspect(w)
		if view.OKHeader && view.Header.Stage == protocol.StageUploading {
			sent += len(view.Payload)
		}
	}
	if sent == 0 {
		t.Fatal("应发出 Stage=1")
	}
	if len(pcm) != sent {
		t.Fatalf("uplink 长度=%d 实际发出=%d，不得约等于 2×", len(pcm), sent)
	}
}

func TestManualReportAfterStopNot202(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_rpst")
	code, body := e.post(t, "/devices/sim_rpst/stop", nil)
	if code != http.StatusOK {
		t.Fatalf("stop %d %s", code, body)
	}
	code, body = e.post(t, "/devices/sim_rpst/report", map[string]any{"playingMode": 2})
	if code == http.StatusAccepted {
		t.Fatalf("已 finalize 不得 202，得到 %d %s", code, body)
	}
}

func newHoleDevice(t *testing.T, id string) *core.DeviceInstance {
	t.Helper()
	yes := true
	cfg := config.Device{
		Enterprise: "demo", DeviceType: "A3", DeviceID: id,
		Action: "chatbot", PlayingMode: 1,
		Audio: config.Audio{
			Format: "pcm", SampleRate: 16000, Channels: 1,
			SampleFormat: "s16le", SliceMs: 100, MaxPayloadSize: 51200,
		},
		Behavior: config.Behavior{
			AutoRegister: &yes, AutoReport: &yes,
			WriteQueueDepth: 256, WriteDrainTimeoutSec: 2,
		},
		UUID:      config.UUIDRange{Min: 1, Max: 2147483647},
		Server:    config.Server{URL: "ws://127.0.0.1:1/"},
		Recording: config.Recording{OutputDir: t.TempDir()},
	}
	d := core.NewDevice(cfg, core.Options{})
	t.Cleanup(func() { d.Shutdown() })
	return d
}

func TestSpeakGenerationChangedDuringCopy409(t *testing.T) {
	e := newEnv(t)
	e.client.Timeout = 15 * time.Second
	e.auto.replyTTS = true
	e.createStartReady(t, "sim_genc")
	assetID := e.uploadWAV(t)
	e.afterStat = func() {
		code, body := e.post(t, "/devices/sim_genc/stop", nil)
		if code != http.StatusOK {
			t.Fatalf("拷贝窗口 stop %d %s", code, body)
		}
		ins2, gen2 := e.startDevice(t, "sim_genc")
		e.waitReady(t, "sim_genc", ins2, gen2)
	}
	code, body := e.post(t, "/devices/sim_genc/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusConflict || !containsBytes(body, "generation_changed") {
		t.Fatalf("拷贝窗口内 stop+start 换代应 409 generation_changed，得到 %d %s", code, body)
	}
	gcode, gbody, _ := e.get(t, "/devices/sim_genc")
	if gcode != http.StatusOK {
		t.Fatalf("GET %d %s", gcode, gbody)
	}
	ins := strField(decodeMap(t, gbody), "instance_id")
	tcode, tbody, _ := e.get(t, "/devices/sim_genc/turns?instance_id="+ins)
	if tcode == http.StatusOK && containsBytes(tbody, "turn_") {
		t.Fatalf("不得把 PCM 打进新实例, turns=%s", tbody)
	}
	c := e.conn("sim_genc")
	if c != nil {
		for _, w := range c.Writes() {
			if len(w) > 0 && w[0] == protocol.FirstAudio {
				t.Fatal("换代后的新实例不得收到旧 Speak 的 PCM")
			}
		}
	}
}

func TestSpeakAndWaitBoundToOwnTurn(t *testing.T) {
	e := newEnv(t)
	e.client.Timeout = 15 * time.Second
	e.auto.replyTTS = true
	body := e.deviceBody("sim_bind")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", map[string]any{"device": body})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_bind")
	e.waitReady(t, "sim_bind", ins, gen)
	assetID := e.uploadWAV(t)
	type result struct {
		code int
		raw  []byte
	}
	done := make(chan result, 1)
	go func() {
		c, b := e.post(t, "/devices/sim_bind/speak_and_wait", map[string]any{
			"asset_id": assetID, "timeout_sec": 8,
		})
		done <- result{c, b}
	}()
	var turnA string
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		tcode, tbody, _ := e.get(t, "/devices/sim_bind/turns?instance_id="+ins)
		if tcode == http.StatusOK {
			m := decodeMap(t, tbody)
			turns, _ := m["turns"].([]any)
			for _, row := range turns {
				rm, _ := row.(map[string]any)
				if strField(rm, "turn_end_reason") != "" {
					turnA = strField(rm, "turn_id")
					break
				}
			}
		}
		if turnA != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if turnA == "" {
		t.Fatal("A 应先到达终态")
	}
	code, raw = e.post(t, "/devices/sim_bind/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("Speak B 应 202，得到 %d %s", code, raw)
	}
	turnB := strField(decodeMap(t, raw), "turn_id")
	select {
	case got := <-done:
		if got.code != http.StatusOK {
			t.Fatalf("speak_and_wait A 应 200，得到 %d %s", got.code, got.raw)
		}
		gotA := strField(decodeMap(t, got.raw), "turn_id")
		if gotA != turnA {
			t.Fatalf("A 的 wait 应返回 A=%s，得到 %s (B=%s)", turnA, gotA, turnB)
		}
		if gotA == turnB {
			t.Fatal("A 的 wait 不得等到 B")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("speak_and_wait A 应返回")
	}
}

func TestOccupiedSlot409BeatsSpeakPermit429(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxConcurrentSpeaking = 1 })
	e.auto.replyTTS = false
	e.createStartReady(t, "sim_slotp")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_slotp/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("第一次 speak 应 202，得到 %d %s", code, body)
	}
	code, body = e.post(t, "/devices/sim_slotp/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusConflict {
		t.Fatalf("槽占用且额度耗尽应 409 不是 %d %s", code, body)
	}
	if containsBytes(body, "speak_permit") {
		t.Fatalf("不得用 429 speak_permit 掩盖槽占用: %s", body)
	}
}

func TestAsyncSpeakGETTurnHasTerminalFields(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := e.deviceBody("sim_async")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", map[string]any{"device": body})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_async")
	e.waitReady(t, "sim_async", ins, gen)
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_async/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("POST speak 应 202，得到 %d %s", code, raw)
	}
	turnID := strField(decodeMap(t, raw), "turn_id")
	if turnID == "" {
		t.Fatal("turn_id 空")
	}
	q := "?instance_id=" + ins
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		gcode, gbody, _ := e.get(t, "/devices/sim_async/turns/"+turnID+q)
		if gcode == http.StatusOK {
			m := decodeMap(t, gbody)
			if strField(m, "turn_end_reason") != "" && strField(m, "uplink_end_reason") != "" && strField(m, "reply_kind") != "" {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("异步 speak 终态后 GET turn 的 turn_end_reason/uplink_end_reason/reply_kind 应非空")
}

func TestGETFramesUsesTurnOutputDirAfterPUT(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := e.deviceBody("sim_odir")
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", map[string]any{"device": body})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_odir")
	e.waitReady(t, "sim_odir", ins, gen)
	assetID := e.uploadWAV(t)
	code, raw = e.post(t, "/devices/sim_odir/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak %d %s", code, raw)
	}
	turnID := strField(decodeMap(t, raw), "turn_id")
	q := "?instance_id=" + ins
	deadline := time.Now().Add(5 * time.Second)
	var framesCode int
	for time.Now().Before(deadline) {
		framesCode, _, _ = e.get(t, "/devices/sim_odir/turns/"+turnID+"/frames"+q)
		if framesCode == http.StatusOK {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if framesCode != http.StatusOK {
		t.Fatalf("PUT 前 GET frames 应 200，得到 %d", framesCode)
	}
	alt := filepath.Join(e.recDir, "alt-after-speak")
	code, pbody := e.put(t, "/devices/sim_odir/config", map[string]any{
		"recording": map[string]any{"output_dir": alt},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT output_dir %d %s", code, pbody)
	}
	fcode, fbody, _ := e.get(t, "/devices/sim_odir/turns/"+turnID+"/frames"+q)
	if fcode != http.StatusOK {
		t.Fatalf("旧 turn GET frames 应仍 200，得到 %d %s", fcode, fbody)
	}
	ucode, ubody, _ := e.get(t, "/devices/sim_odir/turns/"+turnID+"/audio/uplink"+q)
	if ucode != http.StatusOK {
		t.Fatalf("旧 turn GET uplink 应仍 200，得到 %d %s", ucode, ubody)
	}
}

func TestLastActivityRefreshesOnSpeak(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	e.createStartReady(t, "sim_act")
	_, gbody, _ := e.get(t, "/devices/sim_act")
	before := strField(decodeMap(t, gbody), "last_activity")
	if before == "" {
		t.Fatal("start 后 last_activity 应非空")
	}
	t0, err := time.Parse(time.RFC3339Nano, before)
	if err != nil {
		t.Fatalf("parse last_activity %q: %v", before, err)
	}
	time.Sleep(20 * time.Millisecond)
	assetID := e.uploadWAV(t)
	code, raw := e.post(t, "/devices/sim_act/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak %d %s", code, raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, gbody, _ = e.get(t, "/devices/sim_act")
		after := strField(decodeMap(t, gbody), "last_activity")
		t1, err := time.Parse(time.RFC3339Nano, after)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if t1.After(t0) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("start 后 speak，GET last_activity 应变新")
}
