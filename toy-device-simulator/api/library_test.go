package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"toy-device-simulator/media"
)

// makeMP3 用共享工具链生成 100ms mono mp3；无 ffmpeg / 缺编码器则 skip。
func makeMP3(t *testing.T, sr int) []byte {
	t.Helper()
	return makeMP3Dur(t, sr, 100)
}

// makeMP3Dur 指定时长（ms）的 mono mp3（64kbps）。
func makeMP3Dur(t *testing.T, sr, ms int) []byte {
	t.Helper()
	tc := sharedToolchain()
	if tc == nil {
		t.Skip("跳过（无 ffmpeg）")
	}
	if err := tc.CanEncode(media.FormatMP3, sr); err != nil {
		t.Skipf("跳过：%v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	if err := os.WriteFile(src, wavPCMDur(sr, ms), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.mp3")
	spec := media.Spec{Format: media.FormatMP3, SampleRate: sr, Channels: 1, BitrateKbps: 64}
	if err := tc.TranscodeFile(context.Background(), src, dst, spec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAssetLibraryPersistsAcrossRestart(t *testing.T) {
	e := newEnv(t)
	code, body := e.postAssetFields(t, "hello.wav", wavPCM(16000), map[string]string{
		"name": "问候语", "language": "zh",
	})
	if code != http.StatusCreated {
		t.Fatalf("上传应 201，得到 %d body=%s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")

	// 同一 assets_root 重建 Server：库来自 index.json。
	e.srv.Close()
	e.start(t, 0)

	code, lbody, _ := e.get(t, "/assets")
	if code != http.StatusOK {
		t.Fatalf("GET /assets 应 200，得到 %d %s", code, lbody)
	}
	if !containsBytes(lbody, id) || !containsBytes(lbody, "问候语") || !containsBytes(lbody, `"language":"zh"`) {
		t.Fatalf("重启后库应保留条目与元数据：%s", lbody)
	}
	code, cbody, hdr := e.get(t, "/assets/"+id+"/content")
	if code != http.StatusOK || len(cbody) == 0 {
		t.Fatalf("重启后 content 应可取，得到 %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type 应 audio/wav，得到 %s", ct)
	}
}

func TestAssetListFilterByFormatAndLanguage(t *testing.T) {
	e := newEnv(t)
	up := func(name, lang string) {
		code, body := e.postAssetFields(t, name+".wav", wavPCM(16000), map[string]string{
			"name": name, "language": lang,
		})
		if code != http.StatusCreated {
			t.Fatalf("上传 %s 应 201：%d %s", name, code, body)
		}
	}
	up("a", "zh")
	up("b", "en")
	up("c", "zh")

	count := func(q string) int {
		code, body, _ := e.get(t, "/assets"+q)
		if code != http.StatusOK {
			t.Fatalf("GET /assets%s 应 200：%d %s", q, code, body)
		}
		m := decodeMap(t, body)
		arr, _ := m["assets"].([]any)
		return len(arr)
	}
	if n := count(""); n != 3 {
		t.Fatalf("全量应 3 条，得到 %d", n)
	}
	if n := count("?language=zh"); n != 2 {
		t.Fatalf("language=zh 应 2 条，得到 %d", n)
	}
	if n := count("?language=en"); n != 1 {
		t.Fatalf("language=en 应 1 条，得到 %d", n)
	}
	if n := count("?format=wav&language=zh"); n != 2 {
		t.Fatalf("format=wav&language=zh 应 2 条，得到 %d", n)
	}
	if n := count("?format=mp3"); n != 0 {
		t.Fatalf("format=mp3 应 0 条，得到 %d", n)
	}
}

func TestAssetPatchEditsNameAndLanguage(t *testing.T) {
	e := newEnv(t)
	id := e.uploadWAV(t)
	code, body := e.do(t, http.MethodPatch, "/assets/"+id, map[string]any{
		"name": "新名字", "language": "en",
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH 应 200，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "name") != "新名字" || strField(m, "language") != "en" {
		t.Fatalf("PATCH 响应应含更新后的元数据：%s", body)
	}
	code, body = e.do(t, http.MethodPatch, "/assets/"+id, map[string]any{"bogus": 1})
	if code != http.StatusBadRequest {
		t.Fatalf("未知键应 400，得到 %d body=%s", code, body)
	}
	code, body = e.do(t, http.MethodPatch, "/assets/ast_nope", map[string]any{"name": "x"})
	if code != http.StatusNotFound {
		t.Fatalf("不存在 id 应 404，得到 %d body=%s", code, body)
	}
}

func TestUploadWithDeviceIDTranscodesToDeviceSpec(t *testing.T) {
	mp3 := makeMP3(t, 44100) // 44.1k mp3 → 16k pcm 设备：必转码
	e := newEnv(t)
	e.createStartReady(t, "sim_uptc")
	code, body := e.postAssetFields(t, "voice.mp3", mp3, map[string]string{
		"device_id": "sim_uptc", "language": "zh",
	})
	if code != http.StatusCreated {
		t.Fatalf("带 device_id 上传应 201，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "format") != "wav" {
		t.Fatalf("pcm 设备的资产应转成 wav，得到 %s", body)
	}
	if !boolField(m, "transcoded") {
		t.Fatalf("应标记 transcoded=true：%s", body)
	}
	if sr, _ := m["sample_rate"].(float64); int(sr) != 16000 {
		t.Fatalf("应重采样到 16000：%s", body)
	}
	id := strField(m, "asset_id")
	code, body = e.post(t, "/devices/sim_uptc/speak", map[string]any{"asset_id": id})
	if code != http.StatusAccepted {
		t.Fatalf("转码后资产 speak 应 202，得到 %d body=%s", code, body)
	}
}

func TestUploadWithUnknownDeviceID404(t *testing.T) {
	e := newEnv(t)
	code, body := e.postAssetFields(t, "a.wav", wavPCM(16000), map[string]string{"device_id": "sim_nope"})
	if code != http.StatusNotFound {
		t.Fatalf("未知 device_id 应 404，得到 %d body=%s", code, body)
	}
}

func TestSpeakMP3AssetAutoTranscodesWithVariantCache(t *testing.T) {
	mp3 := makeMP3(t, 16000)
	e := newEnv(t)
	// 短超时：TTS 回复后靠 idle timeout 快速终态，speak_and_wait 才能在 8s 内返回。
	dev := e.deviceBody("sim_var")
	beh, _ := dev["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	if code, raw := e.post(t, "/devices", e.createBody(dev)); code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_var")
	e.waitReady(t, "sim_var", ins, gen)
	code, body := e.postAsset(t, "tone.mp3", mp3)
	if code != http.StatusCreated {
		t.Fatalf("mp3 直接入库应 201，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "format") != "mp3" {
		t.Fatalf("无 device_id 上传应保留原格式 mp3：%s", body)
	}
	id := strField(m, "asset_id")
	code, body = e.post(t, "/devices/sim_var/speak_and_wait", map[string]any{
		"asset_id": id, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("mp3 资产喂 pcm 设备应成功，得到 %d body=%s", code, body)
	}
	countVariants := func() int {
		ents, _ := os.ReadDir(e.cfg.AssetsRoot)
		n := 0
		for _, en := range ents {
			if strings.Contains(en.Name(), ".v.") {
				n++
			}
		}
		return n
	}
	if n := countVariants(); n != 1 {
		t.Fatalf("应生成 1 个派生副本，得到 %d", n)
	}
	// 第二次 speak 命中缓存：不再新增派生副本（fakeconn 只回一次 TTS，
	// 这里用异步 speak 即可覆盖缓存路径）。
	code, body = e.post(t, "/devices/sim_var/speak", map[string]any{"asset_id": id})
	if code != http.StatusAccepted {
		t.Fatalf("第二次 speak 应 202，得到 %d body=%s", code, body)
	}
	if n := countVariants(); n != 1 {
		t.Fatalf("缓存命中不应新增派生副本，得到 %d", n)
	}
}
