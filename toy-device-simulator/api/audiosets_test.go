package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 音频集（Phase 13）：增删改查、三种拒绝、被引用的资产删不掉、跨重启。
func TestAudioSets(t *testing.T) {
	e := newEnv(t)
	a1, a2 := e.uploadWAV(t), e.uploadWAV(t)
	img := e.uploadImage(t, "a.png", pngBytes(800))

	// 条目有序、允许重复；名称去首尾空白。
	code, body := e.post(t, "/audio_sets", map[string]any{"name": " 冒烟 ", "asset_ids": []string{a2, a1, a2}})
	m := decodeMap(t, body)
	id := strField(m, "id")
	if code != http.StatusCreated || !strings.HasPrefix(id, "set_") || strField(m, "name") != "冒烟" {
		t.Fatalf("建集应 201，得到 %d %s", code, body)
	}
	if !containsBytes(body, `"asset_ids":["`+a2+`","`+a1+`","`+a2+`"]`) {
		t.Fatalf("条目应保持顺序与重复: %s", body)
	}
	// 不给条目：asset_ids 是 [] 不是 null（UI 要读 .length）。
	if code, body := e.post(t, "/audio_sets", map[string]any{"name": "空集"}); code != http.StatusCreated || !containsBytes(body, `"asset_ids":[]`) {
		t.Fatalf("空集应 201 且 asset_ids=[]，得到 %d %s", code, body)
	}

	for _, c := range []struct {
		why  string
		body map[string]any
		want int
	}{
		{"库里没有", map[string]any{"name": "x", "asset_ids": []string{"ast_nope"}}, http.StatusBadRequest},
		{"图片", map[string]any{"name": "x", "asset_ids": []string{img}}, http.StatusBadRequest},
		{"重名", map[string]any{"name": "冒烟"}, http.StatusConflict},
		{"空名", map[string]any{"name": "  "}, http.StatusBadRequest},
		{"多余字段", map[string]any{"name": "x", "bogus": 1}, http.StatusBadRequest},
	} {
		if code, body := e.post(t, "/audio_sets", c.body); code != c.want {
			t.Fatalf("%s 应 %d，得到 %d %s", c.why, c.want, code, body)
		}
	}

	// PUT 整份替换：只给 name 会被拒，不能顺手清空条目。
	if code, body := e.put(t, "/audio_sets/"+id, map[string]any{"name": "改名"}); code != http.StatusBadRequest {
		t.Fatalf("PUT 缺 asset_ids 应 400，得到 %d %s", code, body)
	}
	if code, body := e.put(t, "/audio_sets/"+id, map[string]any{"name": "回归", "asset_ids": []string{a1}}); code != http.StatusOK || strField(decodeMap(t, body), "name") != "回归" {
		t.Fatalf("PUT 应 200 并改名，得到 %d %s", code, body)
	}

	// 被引用的资产删不掉（不级联，免得悄悄删短回归集）；移出后能删。
	if code, body := e.del(t, "/assets/"+a1); code != http.StatusConflict || !containsBytes(body, "回归") {
		t.Fatalf("被引用的资产应 409 并点名音频集，得到 %d %s", code, body)
	}
	e.put(t, "/audio_sets/"+id, map[string]any{"name": "回归", "asset_ids": []string{a2}})
	if code, body := e.del(t, "/assets/"+a1); code != http.StatusNoContent {
		t.Fatalf("移出后应能删，得到 %d %s", code, body)
	}

	// 跨重启还在；盘上资产文件没了的，加载时从集里剔掉。
	e.srv.Close()
	e.start(t, 0)
	if code, body, _ := e.get(t, "/audio_sets"); code != http.StatusOK || !containsBytes(body, `"asset_ids":["`+a2+`"]`) {
		t.Fatalf("重启后音频集应还在，得到 %d %s", code, body)
	}
	if err := os.Remove(filepath.Join(e.cfg.AssetsRoot, a2+".wav")); err != nil {
		t.Fatal(err)
	}
	e.srv.Close()
	e.start(t, 0)
	if _, body, _ := e.get(t, "/audio_sets"); !containsBytes(body, `"name":"回归","asset_ids":[]`) {
		t.Fatalf("文件没了的资产应被剔除: %s", body)
	}

	// 删集；再删一次 404。
	if code, body := e.del(t, "/audio_sets/"+id); code != http.StatusNoContent {
		t.Fatalf("删集应 204，得到 %d %s", code, body)
	}
	if code, _ := e.del(t, "/audio_sets/"+id); code != http.StatusNotFound {
		t.Fatalf("删不存在的集应 404，得到 %d", code)
	}
}
