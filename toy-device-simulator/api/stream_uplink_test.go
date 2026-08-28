package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

// mp3DeviceBody format=mp3 的设备体（64kbps、短超时保 speak_and_wait 快速终态）。
func (e *testEnv) mp3DeviceBody(id string) map[string]any {
	dev := e.deviceBody(id)
	audio, _ := dev["audio"].(map[string]any)
	audio["format"] = "mp3"
	audio["bitrate_kbps"] = 64
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	return dev
}

func (e *testEnv) createStartReadyMP3(t *testing.T, id string) {
	t.Helper()
	if code, raw := e.post(t, "/devices", e.createBody(e.mp3DeviceBody(id))); code != http.StatusCreated {
		t.Fatalf("mp3 设备创建应 201，得到 %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, id)
	e.waitReady(t, id, ins, gen)
}

// uplinkAudioFrames 从 fakeconn 抓上行音频帧（Stage=1/2），返回 (stage1 帧数, 总 payload 字节, 各帧格式)。
func uplinkAudioFrames(t *testing.T, c *fakeConn) (stage1 int, payloadBytes int, formats map[string]bool, hasFin bool) {
	t.Helper()
	formats = map[string]bool{}
	for _, w := range c.Writes() {
		if len(w) == 0 || w[0] != protocol.FirstAudio {
			continue
		}
		view := protocol.Inspect(w)
		if !view.OKHeader {
			continue
		}
		f := strings.TrimRight(string(view.Header.AudioFormat[:]), "\x00")
		formats[f] = true
		switch view.Header.Stage {
		case protocol.StageUploading:
			stage1++
			payloadBytes += int(view.Header.AudioPayloadLen)
		case protocol.StageFinished:
			hasFin = true
		}
	}
	return stage1, payloadBytes, formats, hasFin
}

func TestCompressedSpeakStreamsMP3WithPacing(t *testing.T) {
	mp3 := makeMP3Dur(t, 16000, 1000)
	e := newEnv(t)
	e.createStartReadyMP3(t, "sim_mp3")

	code, body := e.postAsset(t, "tone.mp3", mp3)
	if code != http.StatusCreated {
		t.Fatalf("mp3 入库应 201，得到 %d %s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")

	start := time.Now()
	code, body = e.post(t, "/devices/sim_mp3/speak_and_wait", map[string]any{
		"asset_id": id, "timeout_sec": 8,
	})
	elapsed := time.Since(start)
	if code != http.StatusOK {
		t.Fatalf("mp3 设备 speak_and_wait 应 200，得到 %d body=%s", code, body)
	}
	// -re 限速：1s 音频至少走 ~0.5s（EOF 提前 ~0.4s，留容差）。
	if elapsed < 400*time.Millisecond {
		t.Fatalf("推流未按实际速率限速：%v", elapsed)
	}

	c := e.conn("sim_mp3")
	stage1, payloadBytes, formats, hasFin := uplinkAudioFrames(t, c)
	if stage1 < 3 {
		t.Fatalf("1s/64kbps 应切出多帧（~10），得到 %d", stage1)
	}
	if payloadBytes < 4000 {
		t.Fatalf("上行总字节应≈8KB，得到 %d", payloadBytes)
	}
	if !formats["mp3"] {
		t.Fatalf("上行帧头 AudioFormat 应为 mp3，得到 %v", formats)
	}
	if !hasFin {
		t.Fatal("应发 Stage=2 收尾帧")
	}
}

func TestCompressedSpeakRejectsStreamBody(t *testing.T) {
	if sharedToolchain() == nil {
		t.Skip("跳过（无 ffmpeg）")
	}
	e := newEnv(t)
	e.createStartReadyMP3(t, "sim_mp3s")
	code, body := e.post(t, "/devices/sim_mp3s/speak", map[string]any{
		"stream": []any{map[string]any{"type": "silence", "duration_ms": 100}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("压缩设备 stream 拼接应 400，得到 %d body=%s", code, body)
	}
}

func TestCompressedSpeakWavAssetAutoTranscodes(t *testing.T) {
	if sharedToolchain() == nil {
		t.Skip("跳过（无 ffmpeg）")
	}
	e := newEnv(t)
	e.createStartReadyMP3(t, "sim_mp3w")
	id := e.uploadWAV(t) // 16k wav 资产 → mp3 设备：选用时转码
	code, body := e.post(t, "/devices/sim_mp3w/speak_and_wait", map[string]any{
		"asset_id": id, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("wav 资产喂 mp3 设备应自动转码并成功，得到 %d body=%s", code, body)
	}
	c := e.conn("sim_mp3w")
	_, _, formats, _ := uplinkAudioFrames(t, c)
	if !formats["mp3"] {
		t.Fatalf("转码后上行帧头应为 mp3，得到 %v", formats)
	}
}

func TestCompressedSpeakInterruptKillsStream(t *testing.T) {
	mp3 := makeMP3Dur(t, 16000, 3000)
	e := newEnv(t)
	e.createStartReadyMP3(t, "sim_mp3i")
	code, body := e.postAsset(t, "long.mp3", mp3)
	if code != http.StatusCreated {
		t.Fatalf("入库 %d %s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")

	code, body = e.post(t, "/devices/sim_mp3i/speak", map[string]any{"asset_id": id})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	turnID := strField(m, "turn_id")
	ins := strField(m, "instance_id")

	time.Sleep(300 * time.Millisecond) // 让推流跑起来
	code, body = e.post(t, "/devices/sim_mp3i/interrupt", map[string]any{
		"instance_id": ins, "turn_id": turnID,
	})
	if code != http.StatusOK {
		t.Fatalf("interrupt 应 200，得到 %d body=%s", code, body)
	}
	// 3s 音频被打断：终态应远早于播完（interrupt 响应即终态，这里再确认设备仍可用）。
	code, gbody, _ := e.get(t, "/devices/sim_mp3i")
	if code != http.StatusOK {
		t.Fatalf("设备应仍 live：%d %s", code, gbody)
	}
}
