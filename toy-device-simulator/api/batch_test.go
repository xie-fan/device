package api

import (
	"net/http"
	"testing"

	"toy-device-simulator/manager"
)

func TestBatchStartAllSuccess202(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_b1")
	e.createDevice(t, "sim_b2")
	code, body := e.post(t, "/devices/batch/start", map[string]any{
		"device_ids": []string{"sim_b1", "sim_b2"}, "stagger_ms": 10,
	})
	if code != http.StatusAccepted {
		t.Fatalf("批量 start 全成功应 202，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	succ, _ := m["succeeded"].([]any)
	fail, _ := m["failed"].([]any)
	if len(succ) != 2 || len(fail) != 0 {
		t.Fatalf("succeeded 应 2 条，failed 应空，body=%s", body)
	}
}

func TestBatchPartialSuccess207(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_p1")
	code, body := e.post(t, "/devices/batch/start", map[string]any{
		"device_ids": []string{"sim_p1", "sim_missing"},
	})
	if code != http.StatusMultiStatus {
		t.Fatalf("部分成功应 207，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	succ, _ := m["succeeded"].([]any)
	fail, _ := m["failed"].([]any)
	if len(succ) == 0 || len(fail) == 0 {
		t.Fatalf("207 应同时有 succeeded 与 failed，body=%s", body)
	}
}

func TestBatchAllPermitExhausted429(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxConnections = 0 })
	e.createDevice(t, "sim_pe1")
	e.createDevice(t, "sim_pe2")
	code, body := e.post(t, "/devices/batch/start", map[string]any{
		"device_ids": []string{"sim_pe1", "sim_pe2"},
	})
	if code != http.StatusTooManyRequests {
		t.Fatalf("批量 start 全员 conn_permit 耗尽应 429，得到 %d body=%s", code, body)
	}
}

func TestConnPermitExceeded429(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxConnections = 1 })
	e.createDevice(t, "sim_c1")
	e.createDevice(t, "sim_c2")
	code, body := e.post(t, "/devices/sim_c1/start", nil)
	if code != http.StatusAccepted {
		t.Fatalf("第一台 start 应 202，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_c2/start", nil)
	if code != http.StatusTooManyRequests {
		t.Fatalf("conn_permit 不足应 429，得到 %d body=%s", code, body)
	}
}

func TestBatchAllFailAny429Is429(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxConnections = 1 })
	e.createStartReady(t, "sim_mx_a")
	e.createDevice(t, "sim_mx_b")
	code, body := e.post(t, "/devices/batch/start", map[string]any{
		"device_ids": []string{"sim_mx_a", "sim_mx_b"},
	})
	if code != http.StatusTooManyRequests {
		t.Fatalf("全失败且含 429 应 429，得到 %d body=%s", code, body)
	}
}

func TestSpeakPermitExceeded429(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxConcurrentSpeaking = 0 })
	e.auto.replyTTS = false
	e.createStartReady(t, "sim_sp")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_sp/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusTooManyRequests {
		t.Fatalf("speak_permit 不足应 429，得到 %d body=%s", code, body)
	}
}
