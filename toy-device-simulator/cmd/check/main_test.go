package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckAcceptsExampleYAML(t *testing.T) {
	root := findRoot(t)
	if code := run([]string{"--config", filepath.Join(root, "configs", "example_device.yaml")}); code != 0 {
		t.Fatalf("exit=%d", code)
	}
}

func TestCheckRejectsNonPCM(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	raw := []byte(`
device:
  enterprise: "demo"
  device_type: "A3"
  device_id: "sim_001"
  action: "chatbot"
  playing_mode: 1
  audio:
    format: "wav"
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
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--config", p}); code == 0 {
		t.Fatal("format≠pcm 应非 0 退出")
	}
}

func TestCheckRejectsMHDeviceType(t *testing.T) {
	p := writeDeviceYAML(t, "MH6W", "ws://127.0.0.1:8089/")
	if code := run([]string{"--config", p}); code == 0 {
		t.Fatal("MH 前缀应非 0 退出")
	}
	if code := run([]string{"--config", p, "--allow-production"}); code == 0 {
		t.Fatal("--allow-production 不得放行 MH")
	}
}

func TestCheckRejectsNonLoopbackWithoutFlag(t *testing.T) {
	p := writeDeviceYAML(t, "A3", "ws://example.com/")
	if code := run([]string{"--config", p}); code == 0 {
		t.Fatal("非 loopback 无 --allow-production 应非 0 退出")
	}
	if code := run([]string{"--config", p, "--allow-production"}); code != 0 {
		t.Fatalf("非 loopback 加 --allow-production 应退出 0，得到 %d", code)
	}
}

func writeDeviceYAML(t *testing.T, deviceType, serverURL string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dev.yaml")
	raw := fmt.Sprintf(`
device:
  enterprise: "demo"
  device_type: %q
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
  server: { url: %q }
`, deviceType, serverURL)
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func findRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(wd) == "check" {
		return filepath.Join(wd, "..", "..")
	}
	return wd
}
