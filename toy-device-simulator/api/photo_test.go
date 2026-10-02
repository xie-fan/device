package api

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

// ---- Phase 14 带图送话与传图留档（phase14.md §3–§5）----

// speakPhotoOverrides 片间隔与收尾计时压短。带图送话不看 features.photo.enabled。
func speakPhotoOverrides() map[string]any {
	return map[string]any{
		"features.photo.slice_interval_ms":   1,
		"behavior.downlink_idle_timeout_sec": 1,
		"behavior.first_reply_timeout_sec":   3,
	}
}

// getPhoto 留档经 recorder 异步落盘：轮询到 200 或超时。
func (e *testEnv) getPhoto(t *testing.T, path string) (int, []byte, http.Header) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, raw, h := e.get(t, path)
		if code == http.StatusOK || time.Now().After(deadline) {
			return code, raw, h
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// photoBeforeAudio 取 uuid 这一轮的图片分片，并断言它们全部排在该 uuid 的音频之前。
func photoBeforeAudio(t *testing.T, c *fakeConn, uuid uint32) (slices []protocol.ImageHeader, joined []byte) {
	t.Helper()
	sawAudio := false
	for _, w := range c.Writes() {
		if len(w) == 0 {
			continue
		}
		switch w[0] {
		case protocol.FirstAudio:
			if h, err := protocol.DecodeHeader(w[1:]); err == nil && h.UUID == uuid {
				sawAudio = true
			}
		case protocol.FirstImage:
			h, err := protocol.DecodeImageHeader(w[1:])
			if err != nil || h.UUID != uuid {
				continue
			}
			if sawAudio {
				t.Fatal("图片分片必须全部排在本轮音频之前")
			}
			slices = append(slices, h)
			joined = append(joined, w[1+protocol.ImageHeaderBytes:]...)
		}
	}
	if !sawAudio {
		t.Fatal("传完图应接着发本轮音频")
	}
	return slices, joined
}

func TestSpeakWithImageUploadsBeforeAudioSameUUID(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	img := jpegBytes(60004) // 两片：51200 + 8804
	imgID := e.uploadImage(t, "cat.jpg", img)
	e.createDevice(t, "sim_sp")
	e.startReadyWithProduct(t, "sim_sp", map[string]any{"overrides": speakPhotoOverrides()})
	ins := strField(e.deviceMap(t, "sim_sp"), "instance_id")

	code, raw := e.post(t, "/devices/sim_sp/speak_and_wait", map[string]any{
		"asset_id": e.uploadWAV(t), "image_asset_id": imgID, "timeout_sec": 10,
	})
	if code != http.StatusOK {
		t.Fatalf("带图送话应 200，得到 %d %s", code, raw)
	}
	res := decodeMap(t, raw)
	if strField(res, "reply_kind") != "tts" {
		t.Fatalf("回复是 UUID 等于本轮的普通 TTS，reply_kind 应为 tts: %v", res)
	}
	uuid := uint32(intField(res, "uplink_uuid"))
	slices, joined := photoBeforeAudio(t, e.conn("sim_sp"), uuid)
	if len(slices) != 2 || !bytes.Equal(joined, img) {
		t.Fatalf("应传 2 片且拼起来等于原图，得到 %d 片", len(slices))
	}
	for _, h := range slices {
		if h.QuestionKey != ([16]byte{}) || h.Reserved != ([40]byte{}) || h.Total != 1 ||
			string(bytes.TrimRight(h.ImageFormat[:], "\x00")) != "jpg" {
			t.Fatalf("带图送话帧头：QuestionKey 与 Reserved 全 0、Total=1、格式 jpg，得到 %+v", h)
		}
	}

	turnID := strField(res, "turn_id")
	want := fmt.Sprintf("source=speak asset=%s uuid=%d bytes=%d slices=2", imgID, uuid, len(img))
	if got := e.turnEvents(t, "sim_sp", ins, turnID)["photo_uploaded"]; got != want {
		t.Fatalf("photo_uploaded 的 reason 应为 %q，得到 %q", want, got)
	}
	path := "/devices/sim_sp/turns/" + turnID + "/photo?instance_id=" + ins
	code, body, h := e.getPhoto(t, path)
	if code != http.StatusOK || h.Get("Content-Type") != "image/jpeg" || !bytes.Equal(body, img) {
		t.Fatalf("省略 uuid 应取带图送话那张：%d %s", code, h.Get("Content-Type"))
	}

	// 盘上历史：manager 重启后旧 instance 的留档照样取得到。
	e.srv.Close()
	e.start(t, 0)
	if code, body, _ := e.get(t, path); code != http.StatusOK || !bytes.Equal(body, img) {
		t.Fatalf("重启后盘上历史也应取得到留档，得到 %d", code)
	}
}

func TestSpeakImageAssetRejects(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_spr")
	e.startReadyWithProduct(t, "sim_spr", nil)
	wav := e.uploadWAV(t)
	for _, c := range []struct {
		img  string
		code int
	}{
		{"ast_nope", http.StatusNotFound},
		{wav, http.StatusBadRequest}, // 音频资产当图用
	} {
		if code, raw := e.post(t, "/devices/sim_spr/speak", map[string]any{"asset_id": wav, "image_asset_id": c.img}); code != c.code {
			t.Fatalf("image_asset_id=%s 应 %d，得到 %d %s", c.img, c.code, code, raw)
		}
	}
	if n := len(imageFrames(e.conn("sim_spr"))); n != 0 {
		t.Fatalf("被拒的请求不得传图，得到 %d 片", n)
	}
}

// 排队的带图送话，出队后照样先传图。
func TestQueuedSpeakWithImageUploadsAfterDequeue(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	img := jpegBytes(1000)
	imgID := e.uploadImage(t, "cat.jpg", img)
	ov := speakPhotoOverrides()
	ov["behavior.speak_backlog_depth"] = 1
	e.createDevice(t, "sim_spq")
	e.startReadyWithProduct(t, "sim_spq", map[string]any{"overrides": ov})
	ins := strField(e.deviceMap(t, "sim_spq"), "instance_id")
	wav := e.uploadWAV(t)

	if code, raw := e.post(t, "/devices/sim_spq/speak", map[string]any{"asset_id": wav}); code != http.StatusAccepted {
		t.Fatalf("第一个 speak 应 202，得到 %d %s", code, raw)
	}
	code, raw := e.post(t, "/devices/sim_spq/speak_and_wait", map[string]any{
		"asset_id": wav, "image_asset_id": imgID, "timeout_sec": 15,
	})
	if code != http.StatusOK {
		t.Fatalf("排队的带图送话应 200，得到 %d %s", code, raw)
	}
	res := decodeMap(t, raw)
	if !boolField(res, "queued") {
		t.Fatalf("槽被占时这一条应先排队: %s", raw)
	}
	turnID := strField(res, "turn_id")
	code, raw, _ = e.get(t, "/devices/sim_spq/turns/"+turnID+"?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("GET turn 应 200，得到 %d %s", code, raw)
	}
	uuid := uint32(intField(decodeMap(t, raw), "uplink_uuid"))
	if slices, joined := photoBeforeAudio(t, e.conn("sim_spq"), uuid); len(slices) != 1 || !bytes.Equal(joined, img) {
		t.Fatalf("出队后应先传 1 片图，得到 %d 片", len(slices))
	}
}

// 传图中途被打断：剩下的分片与音频都不再发。
func TestSpeakWithImageInterruptedMidUpload(t *testing.T) {
	e := newEnv(t)
	imgID := e.uploadImage(t, "big.jpg", jpegBytes(5*protocol.MaxImageSliceBytes)) // 5 片
	ov := speakPhotoOverrides()
	ov["features.photo.slice_interval_ms"] = 300
	e.createDevice(t, "sim_spi")
	e.startReadyWithProduct(t, "sim_spi", map[string]any{"overrides": ov})
	ins := strField(e.deviceMap(t, "sim_spi"), "instance_id")

	code, raw := e.post(t, "/devices/sim_spi/speak", map[string]any{"asset_id": e.uploadWAV(t), "image_asset_id": imgID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d %s", code, raw)
	}
	res := decodeMap(t, raw)
	deadline := time.Now().Add(3 * time.Second)
	for len(imageFrames(e.conn("sim_spi"))) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("第一片图迟迟没发出")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, raw := e.post(t, "/devices/sim_spi/interrupt", map[string]any{"instance_id": ins, "turn_id": strField(res, "turn_id")}); code != http.StatusOK {
		t.Fatalf("interrupt 应 200，得到 %d %s", code, raw)
	}
	time.Sleep(900 * time.Millisecond) // 不打断的话够再发 3 片
	if n := len(imageFrames(e.conn("sim_spi"))); n >= 5 {
		t.Fatalf("打断后不应再传剩下的分片，得到 %d 片", n)
	}
	uuid := uint32(intField(res, "uplink_uuid"))
	for _, w := range e.conn("sim_spi").Writes() {
		if len(w) == 0 || w[0] != protocol.FirstAudio {
			continue
		}
		if h, err := protocol.DecodeHeader(w[1:]); err == nil && h.UUID == uuid && h.Stage == protocol.StageUploading {
			t.Fatal("打断后不应再发本轮音频")
		}
	}
}

// 指令拍照传出的图按上传 UUID 留档，reason 带 source=command。
func TestPhotoCommandArchivedByUploadUUID(t *testing.T) {
	e := newEnv(t)
	e.auto.photoCmd = true
	e.auto.photoReply = true
	img := jpegBytes(1000)
	imgID := e.uploadImage(t, "cat.jpg", img)
	e.createDevice(t, "sim_pca")
	e.startReadyWithProduct(t, "sim_pca", map[string]any{"overrides": photoOverrides(imgID)})
	ins := strField(e.deviceMap(t, "sim_pca"), "instance_id")
	turnID := strField(e.speakWait(t, "sim_pca", e.uploadWAV(t), 15), "turn_id")

	frames := imageFrames(e.conn("sim_pca"))
	if len(frames) != 1 {
		t.Fatalf("应传 1 片，得到 %d", len(frames))
	}
	h, _ := protocol.DecodeImageHeader(frames[0][1:])
	want := fmt.Sprintf("source=command asset=%s uuid=%d bytes=%d slices=1", imgID, h.UUID, len(img))
	if got := e.turnEvents(t, "sim_pca", ins, turnID)["photo_uploaded"]; got != want {
		t.Fatalf("photo_uploaded 的 reason 应为 %q，得到 %q", want, got)
	}
	base := "/devices/sim_pca/turns/" + turnID + "/photo?instance_id=" + ins
	if code, body, _ := e.getPhoto(t, base+"&uuid="+strconv.FormatUint(uint64(h.UUID), 10)); code != http.StatusOK || !bytes.Equal(body, img) {
		t.Fatalf("按上传 UUID 应取得到指令拍照那张，得到 %d", code)
	}
	if code, _, _ := e.get(t, base); code != http.StatusNotFound {
		t.Fatalf("这一轮没有带图送话，省略 uuid 应 404，得到 %d", code)
	}
	if code, _, _ := e.get(t, base+"&uuid=x"); code != http.StatusBadRequest {
		t.Fatalf("uuid 非法应 400，得到 %d", code)
	}
}
