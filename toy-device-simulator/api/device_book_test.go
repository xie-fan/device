package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 12：设备只剩 device_id，属性归产品、挂靠归 start。

func TestPostDeviceIDOnly(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", map[string]any{"device_id": "sim_only"})
	if code != http.StatusCreated {
		t.Fatalf("只给 device_id 应 201，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	ids, _ := m["device_ids"].([]any)
	inst, _ := m["instances"].([]any)
	if len(ids) != 1 || ids[0] != "sim_only" || len(inst) != 1 {
		t.Fatalf("应返回 device_ids 与 instances: %s", raw)
	}
	row, _ := inst[0].(map[string]any)
	if strField(row, "device_id") != "sim_only" || strField(row, "instance_id") == "" {
		t.Fatalf("instances 应带 device_id 与 instance_id: %s", raw)
	}
	dev := e.deviceMap(t, "sim_only")
	if strField(dev, "instance_state") != "created" || strField(dev, "product") != "" {
		t.Fatalf("新建设备应是 created、没有产品: %v", dev)
	}
}

func TestPostDeviceRejectsLegacyAndBadBodies(t *testing.T) {
	e := newEnv(t)
	bad := []map[string]any{
		{"device": map[string]any{"device_id": "sim_x"}},
		{"template_id": "default_a3", "count": 1, "id_prefix": "sim_x"},
		{"device_id": "sim_x", "environment": "local"},
		{"device_id": "sim_x", "enterprise": "demo"},
		{"device_id": "sim_x", "device_type": "A3"},
		{},
		{"device_id": "a/b"},
		{"id_prefix": "sim_x", "count": 0},
	}
	for _, b := range bad {
		if code, raw := e.post(t, "/devices", b); code != http.StatusBadRequest {
			t.Errorf("POST /devices %v 应 400，得到 %d %s", b, code, raw)
		}
	}
	if code, _, _ := e.get(t, "/devices/sim_x"); code != http.StatusNotFound {
		t.Fatalf("被拒的请求不得建出设备，GET 得到 %d", code)
	}
	e.createDevice(t, "sim_dup")
	if code, raw := e.post(t, "/devices", map[string]any{"device_id": "sim_dup"}); code != http.StatusConflict {
		t.Fatalf("重名应 409，得到 %d %s", code, raw)
	}
}

func TestPostDevicesPrefixCount(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", map[string]any{"id_prefix": "sim", "count": 2})
	if code != http.StatusCreated {
		t.Fatalf("前缀＋数量应 201，得到 %d %s", code, raw)
	}
	ids, _ := decodeMap(t, raw)["device_ids"].([]any)
	if len(ids) != 2 || ids[0] != "sim_1" || ids[1] != "sim_2" {
		t.Fatalf("应得 sim_1、sim_2，得到 %s", raw)
	}
	e.createDevice(t, "cf_2")
	if code, raw := e.post(t, "/devices", map[string]any{"id_prefix": "cf", "count": 3}); code != http.StatusConflict {
		t.Fatalf("任一 id 冲突应整批 409，得到 %d %s", code, raw)
	}
	for _, id := range []string{"cf_1", "cf_3"} {
		if code, _, _ := e.get(t, "/devices/"+id); code != http.StatusNotFound {
			t.Fatalf("整批失败不得部分创建 %s，GET 得到 %d", id, code)
		}
	}
}

func TestDeviceBookStoresOnlyIDs(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_file")
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(e.cfg.AssetsRoot), "devices.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "device_id: sim_file") {
		t.Fatalf("设备册应记下 device_id: %s", s)
	}
	for _, leaked := range []string{"audio", "behavior", "nic_iccid", "playing_mode"} {
		if strings.Contains(s, leaked) {
			t.Fatalf("设备册不得再有属性 %s: %s", leaked, s)
		}
	}
}

// 旧文件（Phase 11 及以前）的条目套在 device 里、带全套属性：只取 id，不写迁移代码。
func TestDeviceBookLoadsLegacyShape(t *testing.T) {
	e := newEnv(t)
	e.srv.Close()
	legacy := "devices:\n" +
		"    - environment: 测试\n" +
		"      device:\n" +
		"        enterprise: veepai-test\n" +
		"        device_type: VOICE-TEST\n" +
		"        device_id: legacy_1\n" +
		"        playing_mode: 1\n" +
		"        audio:\n" +
		"            format: amr\n" +
		"            sample_rate: 16000\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(e.cfg.AssetsRoot), "devices.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	e.start(t, 0)
	dev := e.deviceMap(t, "legacy_1")
	if strField(dev, "instance_state") != "created" || strField(dev, "product") != "" {
		t.Fatalf("旧条目应加载成只有 id 的设备: %v", dev)
	}
}

// 模板与设备定义已删除：设备只剩 id，模板没东西可装，也没有「定义」可改。
func TestTemplateAndDefinitionRoutesGone(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_gone12")
	checks := []struct{ method, path string }{
		{http.MethodGet, "/templates"},
		{http.MethodPost, "/templates"},
		{http.MethodGet, "/templates/default_a3"},
		{http.MethodDelete, "/templates/default_a3"},
		{http.MethodGet, "/devices/sim_gone12/definition"},
		{http.MethodPut, "/devices/sim_gone12/definition"},
	}
	for _, c := range checks {
		var body any
		if c.method == http.MethodPost || c.method == http.MethodPut {
			body = map[string]any{}
		}
		if code, raw := e.do(t, c.method, c.path, body); code != http.StatusNotFound {
			t.Errorf("%s %s 应 404，得到 %d %s", c.method, c.path, code, raw)
		}
	}
}
