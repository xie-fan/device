package api

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

// Phase 5e：回放格式感知——下行按 turn.json 记录的帧头格式处理，
// 上行按设备配置格式处理；raw=1 取原始字节，默认解码为 wav 试听。

func TestDownlinkMP3SavedByHeaderAndPlayback(t *testing.T) {
	mp3 := makeMP3Dur(t, 16000, 100)
	e := newEnv(t)
	// pcm 设备 + mp3 帧头下行：证明按 header 而非设备配置判定格式。
	e.auto.replyTTS = true
	e.auto.ttsFormat = "mp3"
	e.auto.ttsPayload = mp3
	dev := e.deviceBody("sim_dnfmt")
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	if code, raw := e.post(t, "/devices", e.createBody(dev)); code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_dnfmt")
	e.waitReady(t, "sim_dnfmt", ins, gen)

	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_dnfmt/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	q := "/devices/sim_dnfmt/turns/" + turnID + "/audio/downlink?instance_id=" + ins

	// 终态 + turn.json + downlink.pcm 均为异步落盘：轮询到 raw 回放就绪。
	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	var ct string
	for time.Now().Before(deadline) {
		var c int
		var hdr http.Header
		c, raw, hdr = e.get(t, q+"&raw=1")
		ct = hdr.Get("Content-Type")
		if c == http.StatusOK && ct == "audio/mpeg" && len(raw) == len(mp3) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ct != "audio/mpeg" {
		t.Fatalf("raw=1 应按 header 格式给 audio/mpeg，得到 %s", ct)
	}
	if !bytes.Equal(raw, mp3) {
		t.Fatalf("raw 字节应与下发的 mp3 一致：len=%d want=%d", len(raw), len(mp3))
	}

	// 默认回放：ffmpeg 解码为 wav。
	code, wav, hdr := e.get(t, q)
	if code != http.StatusOK {
		t.Fatalf("默认回放应 200，得到 %d %s", code, wav)
	}
	if got := hdr.Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("默认回放应 audio/wav，得到 %s", got)
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		t.Fatalf("默认回放应为 RIFF 文件，前 8 字节 %x", wav[:min(8, len(wav))])
	}
}

func TestUplinkWavDeviceServedAsIsNoDoubleHeader(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	dev := e.deviceBody("sim_upwav")
	audio, _ := dev["audio"].(map[string]any)
	audio["format"] = "wav"
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	if code, raw := e.post(t, "/devices", e.createBody(dev)); code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_upwav")
	e.waitReady(t, "sim_upwav", ins, gen)
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_upwav/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait %d %s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")

	deadline := time.Now().Add(5 * time.Second)
	var wav []byte
	for time.Now().Before(deadline) {
		c, b, _ := e.get(t, "/devices/sim_upwav/turns/"+turnID+"/audio/uplink?instance_id="+ins)
		if c == http.StatusOK && len(b) >= 48 {
			wav = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(wav) < 48 {
		t.Fatalf("上行回放未就绪，len=%d", len(wav))
	}
	if string(wav[:4]) != "RIFF" {
		t.Fatalf("wav 设备上行回放应以 RIFF 开头，得到 %x", wav[:4])
	}
	// 旧实现会把已含 RIFF 头的内容再包一层（44 偏移处出现内层 RIFF）。
	if bytes.Contains(wav[4:64], []byte("RIFF")) {
		t.Fatal("上行回放出现双重 RIFF 头：wav 设备内容应原样返回")
	}
}

func TestUplinkCompressedRawAndDecoded(t *testing.T) {
	mp3 := makeMP3Dur(t, 16000, 300)
	e := newEnv(t)
	e.createStartReadyMP3(t, "sim_upmp3")
	code, body := e.postAsset(t, "t.mp3", mp3)
	if code != http.StatusCreated {
		t.Fatalf("入库 %d %s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")
	code, body = e.post(t, "/devices/sim_upmp3/speak_and_wait", map[string]any{
		"asset_id": id, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait %d %s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	ins := strField(decodeMap(t, body), "instance_id")
	q := "/devices/sim_upmp3/turns/" + turnID + "/audio/uplink?instance_id=" + ins

	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	var ct string
	for time.Now().Before(deadline) {
		var c int
		var hdr http.Header
		c, raw, hdr = e.get(t, q+"&raw=1")
		ct = hdr.Get("Content-Type")
		if c == http.StatusOK && len(raw) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ct != "audio/mpeg" {
		t.Fatalf("mp3 设备上行 raw=1 应 audio/mpeg，得到 %s", ct)
	}
	code, wav, hdr := e.get(t, q)
	if code != http.StatusOK || hdr.Get("Content-Type") != "audio/wav" {
		t.Fatalf("mp3 设备上行默认回放应解码为 wav，得到 %d %s", code, hdr.Get("Content-Type"))
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		t.Fatal("解码结果应为 RIFF 文件")
	}
}
