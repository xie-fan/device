package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

// Phase 12 拍照（phase12.md §8）：收到 behavior=601 指令 → 按 ImageHeader 分片传图 →
// 这一轮等 UUID=0 的语音回复。桩的行为见 fakeconn_test.go 的 autoOpts.photo*；
// jpegBytes / uploadImage 在 assets_image_test.go。

// photoOverrides 开拍照、配图、把各计时压短。
func photoOverrides(imageID string) map[string]any {
	return map[string]any{
		"features.photo.enabled":             true,
		"features.photo.image":               imageID,
		"features.photo.slice_interval_ms":   1,
		"behavior.downlink_idle_timeout_sec": 1,
		"behavior.non_audio_followup_sec":    1,
	}
}

func imageFrames(c *fakeConn) [][]byte {
	var out [][]byte
	for _, w := range c.Writes() {
		if len(w) > 0 && w[0] == protocol.FirstImage {
			out = append(out, w)
		}
	}
	return out
}

func (e *testEnv) turnEvents(t *testing.T, id, ins, turnID string) map[string]string {
	t.Helper()
	code, raw, _ := e.get(t, "/devices/"+id+"/events?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("GET events 应 200，得到 %d %s", code, raw)
	}
	out := map[string]string{}
	for _, it := range listField(t, raw, "events") {
		ev, _ := it.(map[string]any)
		if strField(ev, "turn_id") == turnID {
			out[strField(ev, "event_type")] = strField(ev, "reason")
		}
	}
	return out
}

func (e *testEnv) speakWait(t *testing.T, id, assetID string, timeoutSec int) map[string]any {
	t.Helper()
	code, raw := e.post(t, "/devices/"+id+"/speak_and_wait", map[string]any{"asset_id": assetID, "timeout_sec": timeoutSec})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait 应 200，得到 %d %s", code, raw)
	}
	return decodeMap(t, raw)
}

func TestPhotoCommandUploadsImageAndReplyExtendsTurn(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true
	e.auto.photoReply = true
	// 回复比 followup（1s）晚到：没有「等拍照回复」的话，这一轮会先按「只收到指令」收尾。
	e.auto.photoReplyDelay = 1500 * time.Millisecond
	img := jpegBytes(60004) // 两片：51200 + 8804
	imgID := e.uploadImage(t, "cat.jpg", img)
	e.createDevice(t, "sim_photo")
	e.startReadyWithProduct(t, "sim_photo", map[string]any{"overrides": photoOverrides(imgID)})
	ins := strField(e.deviceMap(t, "sim_photo"), "instance_id")

	res := e.speakWait(t, "sim_photo", e.uploadWAV(t), 15)
	turnID := strField(res, "turn_id")
	if strField(res, "reply_kind") != "command+tts" {
		t.Fatalf("拍照指令与之后的 UUID=0 语音都算本轮回复，reply_kind 应为 command+tts，得到 %v", res)
	}

	frames := imageFrames(e.conn("sim_photo"))
	if len(frames) != 2 {
		t.Fatalf("60004 字节的图应分 2 片上传，得到 %d", len(frames))
	}
	var joined []byte
	for i, f := range frames {
		h, err := protocol.DecodeImageHeader(f[1:])
		if err != nil {
			t.Fatal(err)
		}
		wantStage := protocol.ImageStageUploading
		if i == 1 {
			wantStage = protocol.ImageStageFinished
		}
		if h.Stage != wantStage || h.SliceTotal != 2 || h.SliceIndex != uint32(i) || h.TotalSize != uint32(len(img)) ||
			string(bytes.TrimRight(h.QuestionKey[:], "\x00")) != "0123456789abcdef" ||
			string(bytes.TrimRight(h.ImageFormat[:], "\x00")) != "jpg" ||
			string(bytes.TrimRight(h.Reserved[:], "\x00")) != "pcm" {
			t.Fatalf("第 %d 片帧头不符（Reserved 应是设备格式 pcm）: %+v", i, h)
		}
		joined = append(joined, f[1+protocol.ImageHeaderBytes:]...)
	}
	if !bytes.Equal(joined, img) {
		t.Fatal("上传的分片拼起来应等于素材库里的原图")
	}

	code, raw, _ := e.get(t, "/devices/sim_photo/turns/"+turnID+"?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("GET turn 应 200，得到 %d %s", code, raw)
	}
	if m := decodeMap(t, raw); intField(m, "down_bytes") != 4 || strField(m, "down_format") != "pcm" {
		t.Fatalf("UUID=0 的回复应计入本轮下行: %s", raw)
	}
	evs := e.turnEvents(t, "sim_photo", ins, turnID)
	if evs["photo_command"] != "0123456789abcdef" {
		t.Fatalf("应记 photo_command，reason 为 QuestionKey: %v", evs)
	}
	if _, ok := evs["photo_uploaded"]; !ok {
		t.Fatalf("应记 photo_uploaded: %v", evs)
	}
}

func TestPhotoSkippedWhenFeatureOff(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true
	imgID := e.uploadImage(t, "cat.jpg", jpegBytes(1000))
	ov := photoOverrides(imgID)
	ov["features.photo.enabled"] = false
	e.createDevice(t, "sim_poff")
	e.startReadyWithProduct(t, "sim_poff", map[string]any{"overrides": ov})
	ins := strField(e.deviceMap(t, "sim_poff"), "instance_id")

	res := e.speakWait(t, "sim_poff", e.uploadWAV(t), 10)
	if strField(res, "reply_kind") != "command" {
		t.Fatalf("没开拍照：本轮只收到指令，reply_kind 应为 command，得到 %v", res)
	}
	if n := len(imageFrames(e.conn("sim_poff"))); n != 0 {
		t.Fatalf("没开拍照不得传图，得到 %d 片", n)
	}
	evs := e.turnEvents(t, "sim_poff", ins, strField(res, "turn_id"))
	if _, ok := evs["photo_command"]; !ok {
		t.Fatalf("仍应记 photo_command: %v", evs)
	}
	if evs["photo_skipped"] == "" {
		t.Fatalf("应记 photo_skipped 并写原因: %v", evs)
	}
}

func TestPhotoReplyTimeoutEndsTurnAsCommand(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true // 不回 UUID=0 语音
	imgID := e.uploadImage(t, "cat.jpg", jpegBytes(1000))
	ov := photoOverrides(imgID)
	ov["features.photo.reply_timeout_sec"] = 1
	e.createDevice(t, "sim_pto")
	e.startReadyWithProduct(t, "sim_pto", map[string]any{"overrides": ov})

	res := e.speakWait(t, "sim_pto", e.uploadWAV(t), 10)
	if strField(res, "reply_kind") != "command" || strField(res, "turn_end_reason") != "idle" {
		t.Fatalf("等拍照回复超时应按「只收到指令」收尾（idle / command），得到 %v", res)
	}
	if n := len(imageFrames(e.conn("sim_pto"))); n != 1 {
		t.Fatalf("图应已上传 1 片，得到 %d", n)
	}
}

func TestPhotoServerDefaultReplyLeavesReservedEmpty(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true
	e.auto.photoReply = true
	imgID := e.uploadImage(t, "cat.jpg", jpegBytes(1000))
	ov := photoOverrides(imgID)
	ov["features.photo.server_default_reply"] = true
	e.createDevice(t, "sim_psd")
	e.startReadyWithProduct(t, "sim_psd", map[string]any{"overrides": ov})

	e.speakWait(t, "sim_psd", e.uploadWAV(t), 10)
	frames := imageFrames(e.conn("sim_psd"))
	if len(frames) != 1 {
		t.Fatalf("应上传 1 片，得到 %d", len(frames))
	}
	h, _ := protocol.DecodeImageHeader(frames[0][1:])
	if h.Reserved != ([40]byte{}) {
		t.Fatalf("server_default_reply=true 时 Reserved 应全 0: %v", h.Reserved)
	}
}

func TestPhotoStartVoiceSaved(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true
	e.auto.photoStartVoice = []byte{1, 2, 3, 4, 5}
	e.createDevice(t, "sim_psv")
	e.startReadyWithProduct(t, "sim_psv", map[string]any{"overrides": map[string]any{"behavior.non_audio_followup_sec": 1}})
	ins := strField(e.deviceMap(t, "sim_psv"), "instance_id")

	turnID := strField(e.speakWait(t, "sim_psv", e.uploadWAV(t), 10), "turn_id")
	path := filepath.Join(e.recDir, "sim_psv", ins, turnID, "photo_start_voice.pcm")
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := os.ReadFile(path)
		if err == nil && bytes.Equal(got, []byte{1, 2, 3, 4, 5}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("start_voice 应解码存到 %s，得到 %v %v", path, got, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 功能开关运行中可改，下一次收到指令时生效。
func TestPhotoFeatureHotToggleWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true
	e.auto.photoReply = true
	imgID := e.uploadImage(t, "cat.jpg", jpegBytes(1000))
	ov := photoOverrides(imgID)
	ov["features.photo.enabled"] = false
	e.createDevice(t, "sim_phot")
	e.startReadyWithProduct(t, "sim_phot", map[string]any{"overrides": ov})

	if code, raw := e.put(t, "/devices/sim_phot/config", map[string]any{
		"features": map[string]any{"photo": map[string]any{"enabled": true}},
	}); code != http.StatusOK {
		t.Fatalf("运行中打开拍照应 200，得到 %d %s", code, raw)
	}
	e.speakWait(t, "sim_phot", e.uploadWAV(t), 10)
	if n := len(imageFrames(e.conn("sim_phot"))); n != 1 {
		t.Fatalf("运行中打开后，下一次指令应传图，得到 %d 片", n)
	}
}
