package api

import (
	"net/http"
	"testing"
)

// 设备定义要跨 manager 重启存活——agent 按 device_id 引用一台配好的设备，
// 不该每次进程重启都重建。加载回来必须是 created，不能自动 start。
func TestDevicesSurviveRestart(t *testing.T) {
	e := newEnv(t)

	e.createDevice(t, "keep_me")
	// 落盘的是定义；PUT /config 的临时修改不落盘（见 TestConfigOverrideIsNotPersisted）。
	code, _ := e.put(t, "/devices/keep_me/definition", map[string]any{
		"audio": map[string]any{"format": "mp3", "sample_rate": 16000, "bitrate_kbps": 64},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT definition 应 200，得到 %d", code)
	}

	// 同一 assets_root（devices.yaml 与之同目录）重建 Server。
	e.srv.Close()
	e.start(t, 0)

	code, body, _ := e.get(t, "/devices")
	if code != http.StatusOK {
		t.Fatalf("GET /devices 应 200，得到 %d", code)
	}
	if !containsBytes(body, "keep_me") {
		t.Fatalf("重启后设备应还在：%s", body)
	}
	if !containsBytes(body, `"instance_state":"created"`) {
		t.Fatalf("重启后应是 created（不自动 start）：%s", body)
	}
	code, cbody, _ := e.get(t, "/devices/keep_me/config")
	if code != http.StatusOK {
		t.Fatalf("GET config 应 200，得到 %d", code)
	}
	if !containsBytes(cbody, `"format":"mp3"`) || !containsBytes(cbody, `"bitrate_kbps":64`) {
		t.Fatalf("重启后应保留写进定义的音频配置：%s", cbody)
	}
}

// 删除必须同步落盘，否则重启后「删掉的设备」会诈尸。
func TestDeletedDeviceDoesNotComeBack(t *testing.T) {
	e := newEnv(t)

	e.createDevice(t, "gone_soon")
	e.createDevice(t, "stays")
	code, _ := e.del(t, "/devices/gone_soon")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d", code)
	}

	e.srv.Close()
	e.start(t, 0)

	code, body, _ := e.get(t, "/devices")
	if code != http.StatusOK {
		t.Fatalf("GET /devices 应 200，得到 %d", code)
	}
	if containsBytes(body, "gone_soon") {
		t.Fatalf("已删设备不该在重启后回来：%s", body)
	}
	if !containsBytes(body, "stays") {
		t.Fatalf("未删的设备应还在：%s", body)
	}
}
