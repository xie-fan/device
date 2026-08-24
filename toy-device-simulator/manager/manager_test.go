package manager

import (
	"path/filepath"
	"runtime"
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
	if cfg.PerDeviceBufferBytes != 1048576 {
		t.Fatalf("per_device_buffer_bytes 缺省应为 1048576，得到 %d", cfg.PerDeviceBufferBytes)
	}
	if cfg.EventLogMaxEntries != 10000 {
		t.Fatalf("event_log_max_entries 缺省应为 10000，得到 %d", cfg.EventLogMaxEntries)
	}
	if cfg.EventLogTTLHours != 24 {
		t.Fatalf("event_log_ttl_hours 缺省应为 24，得到 %d", cfg.EventLogTTLHours)
	}
	if cfg.AssetsRoot != "./data/assets" {
		t.Fatalf("assets_root 缺省应为 ./data/assets，得到 %s", cfg.AssetsRoot)
	}
	if cfg.MaxAssetBytes != 10485760 {
		t.Fatalf("max_asset_bytes 缺省应为 10485760，得到 %d", cfg.MaxAssetBytes)
	}
	if cfg.MaxAssetDurationSec != 60 {
		t.Fatalf("max_asset_duration_sec 缺省应为 60，得到 %d", cfg.MaxAssetDurationSec)
	}
	if cfg.MaxStreamEntries != 16 {
		t.Fatalf("max_stream_entries 缺省应为 16，得到 %d", cfg.MaxStreamEntries)
	}
	if cfg.MaxStreamDurationSec != 60 {
		t.Fatalf("max_stream_duration_sec 缺省应为 60，得到 %d", cfg.MaxStreamDurationSec)
	}
	if cfg.WaitReadyTimeoutSec != 30 {
		t.Fatalf("wait_ready_timeout_sec 缺省应为 30，得到 %d", cfg.WaitReadyTimeoutSec)
	}
}

func TestRepoManagerYAMLLoads(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "configs", "manager.yaml")
	if _, err := LoadFile(path); err != nil {
		t.Fatalf("LoadFile configs/manager.yaml: %v", err)
	}
}
