package main

import (
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
