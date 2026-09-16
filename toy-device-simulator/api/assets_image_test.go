package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Phase 12 素材库图片（phase12.md §7）：按魔数识别 jpg/png/bmp，kind=image；
// 图片不能拿去送话；content 给原字节；start 时拍照用图必须是图片资产。

// jpegBytes / pngBytes / bmpBytes 造一张「图」：魔数开头，其余是可辨认的字节。
func jpegBytes(n int) []byte { return magicBytes([]byte{0xFF, 0xD8, 0xFF, 0xE0}, n) }
func pngBytes(n int) []byte {
	return magicBytes([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, n)
}
func bmpBytes(n int) []byte { return magicBytes([]byte{'B', 'M'}, n) }

func magicBytes(magic []byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	copy(b, magic)
	return b
}

func (e *testEnv) uploadImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	code, body := e.postAsset(t, name, data)
	if code != http.StatusCreated {
		t.Fatalf("上传图片应 201，得到 %d %s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")
	if id == "" {
		t.Fatalf("asset_id 为空: %s", body)
	}
	return id
}

func assetIDsOf(t *testing.T, body []byte) []string {
	t.Helper()
	var ids []string
	for _, it := range listField(t, body, "assets") {
		m, _ := it.(map[string]any)
		ids = append(ids, strField(m, "asset_id"))
	}
	return ids
}

func TestImageAssetUploadKindAndFormat(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, format string
		data         []byte
	}{
		{"a.jpg", "jpg", jpegBytes(3000)},
		{"a.png", "png", pngBytes(3000)},
		{"a.bmp", "bmp", bmpBytes(3000)},
	}
	for _, c := range cases {
		code, body := e.postAsset(t, c.name, c.data)
		if code != http.StatusCreated {
			t.Fatalf("%s 应 201，得到 %d %s", c.name, code, body)
		}
		m := decodeMap(t, body)
		if strField(m, "kind") != "image" || strField(m, "format") != c.format || intField(m, "bytes") != len(c.data) {
			t.Fatalf("%s 应识别为 kind=image format=%s: %s", c.name, c.format, body)
		}
		_, got, _ := e.get(t, "/assets/"+strField(m, "asset_id"))
		if strField(decodeMap(t, got), "kind") != "image" {
			t.Fatalf("GET 单个资产也应带 kind=image: %s", got)
		}
	}
	code, body := e.postAsset(t, "a.wav", wavPCM(16000))
	if code != http.StatusCreated || strField(decodeMap(t, body), "kind") != "audio" {
		t.Fatalf("音频资产应是 kind=audio，得到 %d %s", code, body)
	}
}

func TestImageAssetListFilterByKind(t *testing.T) {
	e := newEnv(t)
	wavID := e.uploadWAV(t)
	imgID := e.uploadImage(t, "a.jpg", jpegBytes(500))
	_, body, _ := e.get(t, "/assets?kind=image")
	if ids := assetIDsOf(t, body); len(ids) != 1 || ids[0] != imgID {
		t.Fatalf("kind=image 应只列图片，得到 %v", ids)
	}
	_, body, _ = e.get(t, "/assets?kind=audio")
	if ids := assetIDsOf(t, body); len(ids) != 1 || ids[0] != wavID {
		t.Fatalf("kind=audio 应只列音频，得到 %v", ids)
	}
}

func TestImageAssetContentIsRawBytes(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, ctype string
		data        []byte
	}{
		{"a.jpg", "image/jpeg", jpegBytes(2000)},
		{"a.png", "image/png", pngBytes(2000)},
		{"a.bmp", "image/bmp", bmpBytes(2000)},
	}
	for _, c := range cases {
		id := e.uploadImage(t, c.name, c.data)
		for _, q := range []string{"", "?decode=1"} {
			code, got, hdr := e.get(t, "/assets/"+id+"/content"+q)
			if code != http.StatusOK || !bytes.Equal(got, c.data) || hdr.Get("Content-Type") != c.ctype {
				t.Fatalf("%s content%s 应原样返回，Content-Type=%s；得到 %d %s len=%d",
					c.name, q, c.ctype, code, hdr.Get("Content-Type"), len(got))
			}
		}
	}
}

func TestImageAssetKindPersistsAcrossRestart(t *testing.T) {
	e := newEnv(t)
	id := e.uploadImage(t, "a.png", pngBytes(800))
	e.srv.Close()
	e.start(t, 0)
	code, body, _ := e.get(t, "/assets/"+id)
	if m := decodeMap(t, body); code != http.StatusOK || strField(m, "kind") != "image" || strField(m, "format") != "png" {
		t.Fatalf("重启后图片资产应仍是 kind=image format=png，得到 %d %s", code, body)
	}
}

// Phase 12 之前的 index.json 条目没有 kind，一律按音频。
func TestLegacyAssetIndexWithoutKindIsAudio(t *testing.T) {
	e := newEnv(t)
	e.srv.Close()
	wav := wavPCM(16000)
	if err := os.MkdirAll(e.cfg.AssetsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.cfg.AssetsRoot, "ast_legacy.wav"), wav, 0o644); err != nil {
		t.Fatal(err)
	}
	idx, _ := json.Marshal(map[string]any{"assets": []any{map[string]any{
		"id": "ast_legacy", "file": "ast_legacy.wav", "name": "旧资产", "language": "", "format": "wav",
		"bytes": len(wav), "duration_ms": 100, "sample_rate": 16000, "channels": 1,
		"sample_format": "s16le", "bitrate_kbps": 0, "created_at": 1,
	}}})
	if err := os.WriteFile(filepath.Join(e.cfg.AssetsRoot, "index.json"), idx, 0o644); err != nil {
		t.Fatal(err)
	}
	e.start(t, 0)
	code, body, _ := e.get(t, "/assets/ast_legacy")
	if code != http.StatusOK || strField(decodeMap(t, body), "kind") != "audio" {
		t.Fatalf("没有 kind 的旧条目应读成 audio，得到 %d %s", code, body)
	}
}

func TestSpeakRejectsImageAsset(t *testing.T) {
	e := newEnv(t)
	e.createStartReady(t, "sim_spimg")
	imgID := e.uploadImage(t, "a.jpg", jpegBytes(2000))
	code, body := e.post(t, "/devices/sim_spimg/speak_and_wait", map[string]any{"asset_id": imgID, "timeout_sec": 3})
	if code != http.StatusBadRequest || !containsBytes(body, "图片") {
		t.Fatalf("图片资产送话应 400 且说明是图片，得到 %d %s", code, body)
	}
	code, body = e.post(t, "/devices/sim_spimg/speak", map[string]any{
		"stream": []any{map[string]any{"type": "audio", "asset_id": imgID}},
	})
	if code != http.StatusBadRequest || !containsBytes(body, "图片") {
		t.Fatalf("stream 里放图片资产应 400 且说明是图片，得到 %d %s", code, body)
	}
}

func TestStartRejectsNonImagePhotoAsset(t *testing.T) {
	e := newEnv(t)
	wavID := e.uploadWAV(t)
	imgID := e.uploadImage(t, "a.jpg", jpegBytes(500))
	e.createDevice(t, "sim_pimg")
	for _, bad := range []string{wavID, "ast_nope"} {
		if code, _, raw := e.startWithProduct(t, "sim_pimg", map[string]any{
			"overrides": map[string]any{"features.photo.enabled": true, "features.photo.image": bad},
		}); code != http.StatusBadRequest {
			t.Fatalf("拍照用图 %s 不是图片资产，start 应 400，得到 %d %s", bad, code, raw)
		}
	}
	if code, _, raw := e.startWithProduct(t, "sim_pimg", map[string]any{
		"overrides": map[string]any{"features.photo.enabled": true, "features.photo.image": imgID},
	}); code != http.StatusAccepted {
		t.Fatalf("拍照用图是图片资产时 start 应 202，得到 %d %s", code, raw)
	}
}
