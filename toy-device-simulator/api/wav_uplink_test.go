package api

import (
	"bytes"
	"net/http"
	"testing"

	"toy-device-simulator/core"
	"toy-device-simulator/protocol"
)

// Phase 4f wav 推流：线上流整段只加一次 RIFF 头再切片，禁止逐片封装。

func TestWavUplinkEncodedOnceThenSliced(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	dev := e.deviceBody("sim_wav")
	audio, _ := dev["audio"].(map[string]any)
	audio["format"] = "wav"
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", e.createBody(dev))
	if code != http.StatusCreated {
		t.Fatalf("format=wav 创建应 201，得到 %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_wav")
	e.waitReady(t, "sim_wav", ins, gen)
	assetID := e.uploadWAV(t)

	code, sw := e.post(t, "/devices/sim_wav/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait 应 200，得到 %d %s", code, sw)
	}

	// 拼上行 StageUploading 各片 payload：整体应是一个合法 WAV，且仅首片带 RIFF。
	var stream []byte
	nParts := 0
	for _, w := range e.conn("sim_wav").Writes() {
		if len(w) == 0 || w[0] != protocol.FirstAudio {
			continue
		}
		view := protocol.Inspect(w)
		if !view.OKHeader || view.Header.Stage != protocol.StageUploading || len(view.Payload) == 0 {
			continue
		}
		nParts++
		if nParts > 1 && bytes.HasPrefix(view.Payload, []byte("RIFF")) {
			t.Fatal("第 2+ 片不得再带 RIFF 头（禁止逐片封装）")
		}
		stream = append(stream, view.Payload...)
	}
	if nParts < 2 {
		t.Fatalf("100ms+44B 头按 100ms 切片应至少 2 片，得到 %d", nParts)
	}
	if !bytes.HasPrefix(stream, []byte("RIFF")) {
		t.Fatalf("wav 推流整体应以 RIFF 开头: % x", stream[:8])
	}
	p, err := core.DecodeWAV(stream)
	if err != nil {
		t.Fatalf("拼接流应为合法 WAV: %v", err)
	}
	want, _ := core.DecodeWAV(wavPCM(16000))
	if !bytes.Equal(p.Samples, want.Samples) {
		t.Fatalf("解出的 PCM 应与资产一致（%d vs %d 字节）", len(p.Samples), len(want.Samples))
	}
	if p.SampleRate != 16000 || p.Channels != 1 {
		t.Fatalf("WAV 头 fmt 应与设备一致: %+v", p)
	}
}

func TestPcmUplinkHasNoRIFF(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	dev := e.deviceBody("sim_pcm0")
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	code, raw := e.post(t, "/devices", e.createBody(dev))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_pcm0")
	e.waitReady(t, "sim_pcm0", ins, gen)
	assetID := e.uploadWAV(t)
	if code, sw := e.post(t, "/devices/sim_pcm0/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	}); code != http.StatusOK {
		t.Fatalf("speak_and_wait 应 200，得到 %d %s", code, sw)
	}
	for _, w := range e.conn("sim_pcm0").Writes() {
		if len(w) == 0 || w[0] != protocol.FirstAudio {
			continue
		}
		view := protocol.Inspect(w)
		if view.OKHeader && bytes.Contains(view.Payload, []byte("RIFF")) {
			t.Fatal("format=pcm 上行不得含 RIFF")
		}
	}
}
