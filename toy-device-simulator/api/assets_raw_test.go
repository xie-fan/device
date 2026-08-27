package api

import (
	"bytes"
	"net/http"
	"testing"
)

// Phase 4c raw PCM 上传：fmt 三项全给才收，服务端包 WAV 头走既有管线。

func rawFields(sr, ch, sf string) map[string]string {
	m := map[string]string{}
	if sr != "" {
		m["sample_rate"] = sr
	}
	if ch != "" {
		m["channels"] = ch
	}
	if sf != "" {
		m["sample_format"] = sf
	}
	return m
}

func TestRawPCMUploadAndSpeak(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	raw := bytes.Repeat([]byte{1, 0}, 1600) // 100ms 16k mono s16le
	code, body := e.postAssetFields(t, "a.pcm", raw, rawFields("16000", "1", "s16le"))
	if code != http.StatusCreated {
		t.Fatalf("raw PCM 上传应 201，得到 %d %s", code, body)
	}
	m := decodeMap(t, body)
	if intField(m, "sample_rate") != 16000 || intField(m, "channels") != 1 ||
		strField(m, "sample_format") != "s16le" || strField(m, "container") != "wav" {
		t.Fatalf("元数据应与 fmt 一致且 container=wav: %s", body)
	}
	if intField(m, "duration_ms") != 100 {
		t.Fatalf("100ms 时长应为 100，得到 %s", body)
	}
	assetID := strField(m, "asset_id")

	// 包头后的资产可正常 speak。
	dev := e.deviceBody("sim_raw")
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	if code, raw := e.post(t, "/devices", e.createBody(dev)); code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_raw")
	e.waitReady(t, "sim_raw", ins, gen)
	code, sw := e.post(t, "/devices/sim_raw/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK || strField(decodeMap(t, sw), "turn_end_reason") != "idle" {
		t.Fatalf("raw 资产 speak 应正常 idle 收梢，得到 %d %s", code, sw)
	}
}

func TestRawPCMPartialFmt400(t *testing.T) {
	e := newEnv(t)
	raw := bytes.Repeat([]byte{1, 0}, 160)
	for name, f := range map[string]map[string]string{
		"只给 sample_rate":  rawFields("16000", "", ""),
		"缺 sample_format": rawFields("16000", "1", ""),
		"只给 channels":     rawFields("", "1", ""),
	} {
		code, body := e.postAssetFields(t, "a.pcm", raw, f)
		if code != http.StatusBadRequest || !containsBytes(body, "raw PCM 须同时给") {
			t.Fatalf("%s 应 400 提示三项全给，得到 %d %s", name, code, body)
		}
	}
}

func TestRawPCMBadParams400(t *testing.T) {
	e := newEnv(t)
	raw := bytes.Repeat([]byte{1, 0}, 160)
	cases := []struct {
		name   string
		data   []byte
		fields map[string]string
		want   string
	}{
		{"非 s16le", raw, rawFields("16000", "1", "f32le"), "s16le"},
		{"sample_rate 非数", raw, rawFields("abc", "1", "s16le"), "sample_rate"},
		{"channels 为 0", raw, rawFields("16000", "0", "s16le"), "channels"},
		{"RIFF 内容", wavPCM(16000), rawFields("16000", "1", "s16le"), "RIFF"},
		{"奇数长度", []byte{1, 0, 1}, rawFields("16000", "1", "s16le"), "整数倍"},
	}
	for _, tc := range cases {
		code, body := e.postAssetFields(t, "a.pcm", tc.data, tc.fields)
		if code != http.StatusBadRequest || !containsBytes(body, tc.want) {
			t.Fatalf("%s 应 400 含 %q，得到 %d %s", tc.name, tc.want, code, body)
		}
	}
}

func TestPlainWAVUploadUnaffected(t *testing.T) {
	e := newEnv(t)
	code, body := e.postAsset(t, "a.wav", wavPCM(16000))
	if code != http.StatusCreated {
		t.Fatalf("WAV 路径应不受影响 201，得到 %d %s", code, body)
	}
}
