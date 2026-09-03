package api

import (
	"net/http"
	"testing"
)

// PUT /config 是「试这一次」：改内存、不落盘，重启回到定义。
// PUT /definition 才写盘。两者混用时最容易出的错是把当前值当定义存下去。
func TestConfigOverrideIsNotPersisted(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "dev1")

	code, _ := e.put(t, "/devices/dev1/config", map[string]any{
		"audio": map[string]any{"format": "mp3", "sample_rate": 16000, "bitrate_kbps": 64},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT config 应 200，得到 %d", code)
	}
	code, body, _ := e.get(t, "/devices/dev1/config")
	if code != http.StatusOK || !containsBytes(body, `"format":"mp3"`) {
		t.Fatalf("当前值应已是 mp3：%d %s", code, body)
	}
	if !containsBytes(body, `"overridden":true`) {
		t.Fatalf("偏离定义时 overridden 应为 true：%s", body)
	}
	// 定义没被动过。
	code, dbody, _ := e.get(t, "/devices/dev1/definition")
	if code != http.StatusOK || !containsBytes(dbody, `"format":"pcm"`) {
		t.Fatalf("定义仍应是 pcm：%d %s", code, dbody)
	}

	e.srv.Close()
	e.start(t, 0)

	code, body, _ = e.get(t, "/devices/dev1/config")
	if code != http.StatusOK || !containsBytes(body, `"format":"pcm"`) {
		t.Fatalf("重启后应回到定义 pcm：%d %s", code, body)
	}
	if !containsBytes(body, `"overridden":false`) {
		t.Fatalf("重启后当前值即定义，overridden 应为 false：%s", body)
	}
}

func TestPutDefinitionPersistsAndPullsCurrentAlong(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "dev2")

	code, body := e.put(t, "/devices/dev2/definition", map[string]any{
		"audio": map[string]any{"format": "amr", "sample_rate": 16000, "bitrate_kbps": 23.85},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT definition 应 200，得到 %d %s", code, body)
	}
	// created 状态下当前值被拉齐，所以没有偏离。
	code, cbody, _ := e.get(t, "/devices/dev2/config")
	if !containsBytes(cbody, `"format":"amr"`) || !containsBytes(cbody, `"overridden":false`) {
		t.Fatalf("created 下改定义应同时拉齐当前值：%s", cbody)
	}

	e.srv.Close()
	e.start(t, 0)

	code, cbody, _ = e.get(t, "/devices/dev2/config")
	if code != http.StatusOK || !containsBytes(cbody, `"format":"amr"`) {
		t.Fatalf("重启后应保留定义 amr：%d %s", code, cbody)
	}
}

func TestResetConfigReturnsToDefinition(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "dev3")

	if code, _ := e.put(t, "/devices/dev3/config", map[string]any{
		"audio": map[string]any{"format": "aac", "sample_rate": 16000, "bitrate_kbps": 96},
	}); code != http.StatusOK {
		t.Fatalf("PUT config 应 200，得到 %d", code)
	}
	code, body := e.post(t, "/devices/dev3/config/reset", nil)
	if code != http.StatusOK {
		t.Fatalf("reset 应 200，得到 %d %s", code, body)
	}
	if !containsBytes(body, `"format":"pcm"`) || !containsBytes(body, `"overridden":false`) {
		t.Fatalf("reset 后应回到定义：%s", body)
	}
}
