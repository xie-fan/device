package config

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		{"MH 前缀", func(d *Device) { d.DeviceType = "MH6W" }, "MH"},
		{"channels 非 1", func(d *Device) { d.Audio.Channels = 2 }, "channels"},
		{"sample_format 非 s16le", func(d *Device) { d.Audio.SampleFormat = "f32" }, "sample_format"},
		{"slice_ms 为 0", func(d *Device) { d.Audio.SliceMs = 0 }, "slice_ms"},
		{"auto_register false", func(d *Device) { f := false; d.Behavior.AutoRegister = &f }, "auto_register"},
		{"auto_report false", func(d *Device) { f := false; d.Behavior.AutoReport = &f }, "auto_report"},
		{"device_id 斜杠逃逸", func(d *Device) { d.DeviceID = "../etc" }, "device_id"},
		{"device_id 反斜杠逃逸", func(d *Device) { d.DeviceID = `..\etc` }, "device_id"},
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

func TestTrackedConfigYAMLsHaveNoMHAndLoopbackOnly(t *testing.T) {
	root := moduleRoot(t)
	files := listedConfigYAMLs(t)
	if len(files) == 0 {
		t.Fatal("未找到 configs YAML")
	}
	sawDevice := false
	for _, rel := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		slash := filepath.ToSlash(rel)
		base := filepath.Base(rel)
		switch {
		case base == "manager.yaml":
			// manager.yaml 由 manager 包 LoadFile 校验，避免 config 测试 import 循环。
			continue
		case strings.Contains(slash, "/templates/"):
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				continue
			}
			if yamlHasKey(raw, "device_id") {
				t.Errorf("%s: 模板禁止 device_id", rel)
				continue
			}
			if yamlHasKey(raw, "write_queue_depth") || yamlHasKey(raw, "write_drain_timeout_sec") {
				t.Errorf("%s: 模板禁止 write_queue_*", rel)
				continue
			}
			filled, err := fillTemplateDeviceID(raw, "sim_gate")
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				continue
			}
			d, err := LoadPhase2(filled)
			if err != nil {
				t.Errorf("%s: 填 ID 后 LoadPhase2: %v", rel, err)
				continue
			}
			assertNoMHAndLoopback(t, rel, d)
		default:
			sawDevice = true
			d, err := LoadFile(path)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				continue
			}
			assertNoMHAndLoopback(t, rel, d)
		}
	}
	if !sawDevice {
		t.Fatal("应至少跟踪一份设备 YAML")
	}
}

func assertNoMHAndLoopback(t *testing.T, rel string, d Device) {
	t.Helper()
	if strings.HasPrefix(d.DeviceType, "MH") {
		t.Errorf("%s: device_type 不得以 MH 开头", rel)
	}
	u, err := url.Parse(d.Server.URL)
	if err != nil {
		t.Errorf("%s: 解析 server.url: %v", rel, err)
		return
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		t.Errorf("%s: server.url 必须是 loopback", rel)
	}
}

func fillTemplateDeviceID(raw []byte, id string) ([]byte, error) {
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	dev, _ := root["device"].(map[string]any)
	if dev == nil {
		return nil, fmt.Errorf("缺 device")
	}
	dev["device_id"] = id
	beh, _ := dev["behavior"].(map[string]any)
	if beh == nil {
		beh = map[string]any{}
		dev["behavior"] = beh
	}
	beh["write_queue_depth"] = 256
	beh["write_drain_timeout_sec"] = 2
	return yaml.Marshal(root)
}

func yamlHasKey(raw []byte, key string) bool {
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return false
	}
	return yamlValueHasKey(v, key)
}

func yamlValueHasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == key {
				return true
			}
			if yamlValueHasKey(val, key) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if yamlValueHasKey(val, key) {
				return true
			}
		}
	}
	return false
}

func listedConfigYAMLs(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	seen := map[string]struct{}{}
	var files []string
	add := func(rel string) {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" {
			return
		}
		if !strings.HasSuffix(rel, ".yaml") && !strings.HasSuffix(rel, ".yml") {
			return
		}
		if _, ok := seen[rel]; ok {
			return
		}
		seen[rel] = struct{}{}
		files = append(files, rel)
	}
	cmd := exec.Command("git", "ls-files", "--", "configs")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		add(line)
	}
	// 未跟踪的 manager.yaml / templates 也要过门禁；*.local.yaml 是本机覆盖，不扫。
	add("configs/manager.yaml")
	tmplDir := filepath.Join(root, "configs", "templates")
	ents, _ := os.ReadDir(tmplDir)
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		add(filepath.ToSlash(filepath.Join("configs", "templates", ent.Name())))
	}
	var outFiles []string
	for _, rel := range files {
		base := filepath.Base(rel)
		if strings.HasSuffix(base, ".local.yaml") || strings.HasSuffix(base, ".local.yml") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			continue
		}
		outFiles = append(outFiles, rel)
	}
	return outFiles
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..")
}

func TestValidatePhase2AllowsJSONAckAndNonZeroSleepMs(t *testing.T) {
	d, err := Load(exampleYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	d.Behavior.DownlinkAck.Mode = "json"
	d.Behavior.DownlinkAck.SleepMs = 500
	if err := Validate(d); err == nil {
		t.Fatal("Phase 1 Validate 必须继续拒绝 json")
	}
	if err := ValidatePhase2(d); err != nil {
		t.Fatalf("ValidatePhase2 应允许 json 与 sleep_ms=500: %v", err)
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

func TestLoadDefaultsOmittedAutoRegisterReportToTrue(t *testing.T) {
	d, err := Load(minimalYAMLWithoutAuto())
	if err != nil {
		t.Fatal(err)
	}
	if d.Behavior.AutoRegister == nil || !*d.Behavior.AutoRegister {
		t.Fatal("省略 auto_register 应缺省 true")
	}
	if d.Behavior.AutoReport == nil || !*d.Behavior.AutoReport {
		t.Fatal("省略 auto_report 应缺省 true")
	}
}

func TestLoadRejectsExplicitAutoRegisterFalse(t *testing.T) {
	src := strings.Replace(string(minimalYAMLWithoutAuto()), "write_queue_depth: 256", "auto_register: false\n    write_queue_depth: 256", 1)
	if _, err := Load([]byte(src)); err == nil {
		t.Fatal("auto_register: false 应拒绝")
	} else if !strings.Contains(err.Error(), "auto_register") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidatePathComponentRejectsEscape(t *testing.T) {
	for _, id := range []string{"../etc", `..\etc`, "..", "/abs", `C:\Windows`, "a/b", "a\\b", "a\x00b", "", "C:foo"} {
		if err := ValidatePathComponent(id); err == nil {
			t.Errorf("%q 应拒绝", id)
		}
	}
	if err := ValidatePathComponent("sim_001"); err != nil {
		t.Fatal(err)
	}
}

func minimalYAMLWithoutAuto() []byte {
	return []byte(`
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
}
