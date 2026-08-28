package api

import (
	"net/http"
	"testing"
	"time"

	"toy-device-simulator/manager"
)

func TestSpeakAssetOrStreamXOR400(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_xor")
	code, body := e.post(t, "/devices/sim_xor/speak", map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("asset_id 与 stream 都缺应 400，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_xor/speak", map[string]any{
		"asset_id": "ast_x",
		"stream":   []any{map[string]any{"type": "silence", "duration_ms": 10}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("asset_id 与 stream 同时给应 400，得到 %d body=%s", code, body)
	}
}

func TestSpeakStreamTooManyEntries400(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxStreamEntries = 16 })
	e.createStartReady(t, "sim_se")
	entries := make([]any, 17)
	for i := range entries {
		entries[i] = map[string]any{"type": "silence", "duration_ms": 10}
	}
	code, body := e.post(t, "/devices/sim_se/speak", map[string]any{"stream": entries})
	if code != http.StatusBadRequest {
		t.Fatalf("stream.length > max_stream_entries 应 400，得到 %d body=%s", code, body)
	}
}

func TestSpeakStreamDurationExceedsMax400(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxStreamDurationSec = 1 })
	e.createStartReady(t, "sim_sd")
	code, body := e.post(t, "/devices/sim_sd/speak", map[string]any{
		"stream": []any{map[string]any{"type": "silence", "duration_ms": 1500}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("stream 总时长超 max_stream_duration_sec 应 400，得到 %d body=%s", code, body)
	}
}

// Phase 5c：采样率不符的资产不再直接 400——有 ffmpeg 时自动重采样转码
// （派生副本缓存），无 ffmpeg 时保持 400。
func TestSpeakWAVFmtMismatchAutoTranscodes(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_fmt")
	code, body := e.postAsset(t, "8k.wav", wavPCM(8000))
	if code != http.StatusCreated {
		t.Fatalf("8k WAV 仍应能上传，得到 %d body=%s", code, body)
	}
	assetID := strField(decodeMap(t, body), "asset_id")
	code, body = e.post(t, "/devices/sim_fmt/speak", map[string]any{"asset_id": assetID})
	if sharedToolchain() != nil {
		if code != http.StatusAccepted {
			t.Fatalf("有 ffmpeg 时 fmt 不符应自动转码并 202，得到 %d body=%s", code, body)
		}
		return
	}
	if code != http.StatusBadRequest {
		t.Fatalf("无 ffmpeg 时 fmt 不符应 400，得到 %d body=%s", code, body)
	}
}

func TestSpeakNotSpeakable409(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_ns")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_ns/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusConflict || !containsBytes(body, "not_speakable") {
		t.Fatalf("Created 不可 speak，应 409 not_speakable，得到 %d body=%s", code, body)
	}
}

func TestSpeakAssetEpochChangedDuringCopy404NoCAS(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_ep")
	assetID := e.uploadWAV(t)
	e.afterStat = func() {
		_, _ = e.del(t, "/assets/"+assetID)
	}
	code, body := e.post(t, "/devices/sim_ep/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusNotFound {
		t.Fatalf("拷贝窗口内 epoch 变应 404 且不 CAS，得到 %d body=%s", code, body)
	}
	gcode, gbody, _ := e.get(t, "/devices/sim_ep")
	if gcode != http.StatusOK {
		t.Fatalf("设备应仍 live，GET %d %s", gcode, gbody)
	}
	ins := strField(decodeMap(t, gbody), "instance_id")
	tcode, tbody, _ := e.get(t, "/devices/sim_ep/turns?instance_id="+ins)
	if tcode == http.StatusOK && containsBytes(tbody, "trn_") {
		t.Fatalf("不得留下 Reserved 槽/Turn，turns=%s", tbody)
	}
}

func TestSpeakAccepted202(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	e.createStartReady(t, "sim_oksp")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_oksp/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "turn_id") == "" || strField(m, "instance_id") == "" {
		t.Fatalf("202 应含 turn_id 与 instance_id，body=%s", body)
	}
}

func TestSpeakAndWaitDefaultBudgetFollowsIdleNotHardcoded30s(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = false
	body := e.deviceBody("sim_swb")
	beh, _ := body["behavior"].(map[string]any)
	beh["first_reply_timeout_sec"] = 1
	beh["downlink_idle_timeout_sec"] = 1
	beh["non_audio_followup_sec"] = 0
	beh["post_final_asr_silence_sec"] = 0
	beh["wait_timeout_slack_sec"] = 0
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_swb")
	e.waitReady(t, "sim_swb", ins, gen)
	assetID := e.uploadWAV(t)
	start := time.Now()
	code, raw = e.post(t, "/devices/sim_swb/speak_and_wait", map[string]any{"asset_id": assetID})
	elapsed := time.Since(start)
	if elapsed > 6*time.Second {
		t.Fatalf("默认预算应随 idle/first_reply 走，不得写死 30s，耗时 %s body=%s", elapsed, raw)
	}
	// replyTTS=false 时设备可能先用 first_reply/idle 收口（200 turn_terminal），
	// 或 WaitTurn 先耗尽预算（504）。两种都证明 HTTP 没用写死的 30s。
	if code == http.StatusGatewayTimeout {
		return
	}
	if code != http.StatusOK {
		t.Fatalf("未给 timeout_sec 时应 504 或快速 200，得到 %d body=%s", code, raw)
	}
	if strField(decodeMap(t, raw), "event_type") != "turn_terminal" {
		t.Fatalf("快速 200 应为 turn_terminal，body=%s", raw)
	}
}

func TestSpeakAndWaitTimeout504TurnContinues(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = false
	e.createStartReady(t, "sim_sw")
	assetID := e.uploadWAV(t)
	start := time.Now()
	code, body := e.post(t, "/devices/sim_sw/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 1,
	})
	if code != http.StatusGatewayTimeout {
		t.Fatalf("speak_and_wait 超时应 504，得到 %d body=%s", code, body)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("504 不应远超 timeout_sec")
	}
	code2, body2 := e.post(t, "/devices/sim_sw/speak", map[string]any{"asset_id": assetID})
	if code2 != http.StatusConflict {
		t.Fatalf("504 后 Turn 应继续占槽，再 speak 应 409，得到 %d body=%s", code2, body2)
	}
}
