package manager

import (
	"strings"
	"testing"
)

func TestManagerYAMLRejectsWriteQueueDepthLessThan2(t *testing.T) {
	_, err := Load([]byte(`
manager:
  write_queue_depth: 1
  write_drain_timeout_sec: 2
`))
	if err == nil {
		t.Fatal("write_queue_depth < 2 应加载失败")
	}
	if !strings.Contains(err.Error(), "write_queue_depth") {
		t.Fatalf("err=%v，应含 write_queue_depth", err)
	}
}

func TestManagerYAMLRejectsWriteDrainTimeoutSecNonPositive(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		_, err := Load([]byte(`
manager:
  write_queue_depth: 256
  write_drain_timeout_sec: ` + v + `
`))
		if err == nil {
			t.Fatalf("write_drain_timeout_sec=%s 应加载失败", v)
		}
		if !strings.Contains(err.Error(), "write_drain_timeout_sec") {
			t.Fatalf("err=%v，应含 write_drain_timeout_sec", err)
		}
	}
}

func TestDeviceYAMLWithWriteQueueFieldsRejectedInPhase2Load(t *testing.T) {
	raw := []byte(`
device:
  enterprise: "demo"
  device_type: "A3"
  device_id: "sim_001"
  action: "chatbot"
  playing_mode: 1
  audio:
    format: "pcm"
    sample_rate: 16000
    channels: 1
    sample_format: "s16le"
    slice_ms: 100
    max_payload_size: 51200
  behavior:
    write_queue_depth: 256
    write_drain_timeout_sec: 2
    downlink_ack: { mode: binary, sleep_ms: 0, code: 0 }
  uuid: { min: 1, max: 2147483647 }
  server: { url: "ws://127.0.0.1:8089/" }
`)
	_, err := LoadDeviceYAML(raw)
	if err == nil {
		t.Fatal("Phase 2 设备配置出现 write_queue_* 应拒绝")
	}
}

func TestManagerDefaultStaggerAndPermitsLoad(t *testing.T) {
	cfg, err := Load([]byte(`
manager:
  write_queue_depth: 256
  write_drain_timeout_sec: 2
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultStaggerMs != 50 {
		t.Fatalf("default_stagger_ms 缺省应为 50，得到 %d", cfg.DefaultStaggerMs)
	}
	if cfg.MaxConnections != 32 {
		t.Fatalf("max_connections 缺省应为 32，得到 %d", cfg.MaxConnections)
	}
	if cfg.MaxConcurrentSpeaking != 8 {
		t.Fatalf("max_concurrent_speaking 缺省应为 8，得到 %d", cfg.MaxConcurrentSpeaking)
	}
}
