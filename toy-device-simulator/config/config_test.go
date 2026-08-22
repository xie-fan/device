package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func exampleYAML(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "configs", "example_device.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestExampleDeviceYAMLIsAccepted(t *testing.T) {
	d, err := Load(exampleYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.DeviceType != "A3" {
		t.Fatalf("device_type=%s（不要用 MH 前缀）", d.DeviceType)
	}
	if strings.HasPrefix(d.DeviceType, "MH") {
		t.Fatal("示例机型不得用 MH 前缀，否则 Seq 用例会假通过")
	}
}

func TestValidateRejectsPhase1IllegalConfigs(t *testing.T) {
	base := func() Device {
		d, err := Load(exampleYAML(t))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := []struct {
		name string
		mut  func(*Device)
		want string
	}{
		{"非 pcm", func(d *Device) { d.Audio.Format = "wav" }, "pcm"},
		{"非法 playing_mode", func(d *Device) { d.PlayingMode = 0 }, "playing_mode"},
		{"json ACK", func(d *Device) { d.Behavior.DownlinkAck.Mode = "json" }, "json ACK"},
		{"sleep_ms 非 0", func(d *Device) { d.Behavior.DownlinkAck.SleepMs = 500 }, "sleep_ms"},
		{"depth 小于 2", func(d *Device) { d.Behavior.WriteQueueDepth = 1 }, "write_queue_depth"},
		{"drain 为 0", func(d *Device) { d.Behavior.WriteDrainTimeoutSec = 0 }, "write_drain_timeout_sec"},
		{"action 非 chatbot", func(d *Device) { d.Action = "ipc" }, "chatbot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := base()
			tc.mut(&d)
			err := Validate(d)
			if err == nil {
				t.Fatal("应拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, 应含 %q", err, tc.want)
			}
		})
	}
}

func TestPlayingMode123AreLegal(t *testing.T) {
	d, err := Load(exampleYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []int{1, 2, 3} {
		d.PlayingMode = mode
		if err := Validate(d); err != nil {
			t.Fatalf("mode=%d: %v", mode, err)
		}
	}
}
