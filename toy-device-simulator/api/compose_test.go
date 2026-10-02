package api

import (
	"bytes"
	"net/http"
	"testing"

	"toy-device-simulator/core"
)

// paddedTone 造「前静音 + 一段响声 + 后静音」的 16k 单声道 WAV。
func paddedTone(leadMs, toneMs, tailMs int) []byte {
	ms := func(n int) int { return 16000 * 2 * n / 1000 }
	s := make([]byte, ms(leadMs))
	s = append(s, bytes.Repeat([]byte{0x10, 0x27}, ms(toneMs)/2)...) // 10000
	s = append(s, make([]byte, ms(tailMs))...)
	return core.EncodeWAV(core.PCM{Samples: s, SampleRate: 16000, Channels: 1, BitsPerSample: 16})
}

func TestComposeAsset(t *testing.T) {
	e := newEnv(t)
	up := func(name string, data []byte, tags string) string {
		code, body := e.postAssetFields(t, name+".wav", data, map[string]string{"name": name, "tags": tags})
		if code != http.StatusCreated {
			t.Fatalf("上传 %s 应 201: %d %s", name, code, body)
		}
		return strField(decodeMap(t, body), "asset_id")
	}
	// 两条都带 1 秒前后静音、各 500ms 响声。
	a := up("你好", paddedTone(1000, 500, 1000), "对话")
	b := up("讲个故事", paddedTone(1000, 500, 1000), "故事")

	code, body := e.post(t, "/assets/compose", map[string]any{"asset_ids": []string{a, b}})
	m := decodeMap(t, body)
	if code != http.StatusCreated || strField(m, "name") != "你好+讲个故事" {
		t.Fatalf("组合应 201、名字按来源拼: %d %s", code, body)
	}
	// 每段切到 500ms + 两头各 60ms，再加整条 600ms + 1200ms：2×620 + 1800 = 3040ms。
	// 不切静音就是 2×2500 + 1800 = 6800ms。
	if d := intField(m, "duration_ms"); d < 2900 || d > 3200 {
		t.Fatalf("首尾静音应被切掉，duration_ms=%d", d)
	}
	if !containsBytes(body, `"tags":["对话","故事","组合"]`) || !containsBytes(body, `"composed_of":["`+a+`","`+b+`"]`) {
		t.Fatalf("标签取并集加「组合」、记来源: %s", body)
	}

	// 同来源同顺序：复用，不新建。
	code, body = e.post(t, "/assets/compose", map[string]any{"asset_ids": []string{a, b}})
	if code != http.StatusOK || !boolField(decodeMap(t, body), "reused") || strField(decodeMap(t, body), "asset_id") != strField(m, "asset_id") {
		t.Fatalf("同组合应复用: %d %s", code, body)
	}

	silent := up("静音", paddedTone(500, 0, 500), "")
	for _, c := range []struct {
		why  string
		ids  []string
		want int
	}{
		{"只有一条", []string{a}, http.StatusBadRequest},
		{"库里没有", []string{a, "ast_nope"}, http.StatusNotFound},
		{"整段静音", []string{a, silent}, http.StatusBadRequest},
	} {
		if code, body := e.post(t, "/assets/compose", map[string]any{"asset_ids": c.ids}); code != c.want {
			t.Fatalf("%s 应 %d，得到 %d %s", c.why, c.want, code, body)
		}
	}
}
