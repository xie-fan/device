package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPostDeviceReturns201InstanceID(t *testing.T) {
	e := newEnv(t)
	code, body := e.post(t, "/devices", e.createBody(e.deviceBody("sim_001")))
	if code != http.StatusCreated {
		t.Fatalf("POST /devices 应 201，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	inst, _ := m["instances"].([]any)
	if len(inst) == 0 {
		t.Fatalf("应返回 instances，body=%s", body)
	}
	row, _ := inst[0].(map[string]any)
	if strField(row, "instance_id") == "" || strField(row, "device_id") != "sim_001" {
		t.Fatalf("应返回非空 instance_id 与 device_id，body=%s", body)
	}
}

func TestPostDevicesBatchIDConflict409WholeBatch(t *testing.T) {
	e := newEnv(t)
	tmpl := e.deviceBody("ignored")
	delete(tmpl, "device_id")
	code, body := e.postTemplate(t, "default_a3", tmpl)
	if code != http.StatusCreated {
		t.Fatalf("POST /templates 应 201，得到 %d body=%s", code, body)
	}
	e.createDevice(t, "sim_cf_1")
	code, body = e.post(t, "/devices", map[string]any{
		"template_id": "default_a3",
		"count":       2,
		"id_prefix":   "sim_cf",
	})
	if code != http.StatusConflict {
		t.Fatalf("批量 ID 冲突应整批 409，得到 %d body=%s", code, body)
	}
	code, _, _ = e.get(t, "/devices/sim_cf_2")
	if code != http.StatusNotFound {
		t.Fatalf("整批失败不得部分创建 sim_cf_2，GET 得到 %d", code)
	}
}

func TestPostDevicesBatchIDPrefixUsesUnderscore(t *testing.T) {
	e := newEnv(t)
	tmpl := e.deviceBody("ignored")
	delete(tmpl, "device_id")
	code, body := e.postTemplate(t, "default_a3", tmpl)
	if code != http.StatusCreated {
		t.Fatalf("POST /templates 应 201，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices", map[string]any{
		"template_id": "default_a3",
		"count":       1,
		"id_prefix":   "sim",
	})
	if code != http.StatusCreated {
		t.Fatalf("批量创建应 201，得到 %d body=%s", code, body)
	}
	ids, _ := decodeMap(t, body)["device_ids"].([]any)
	if len(ids) != 1 || fmt.Sprint(ids[0]) != "sim_1" {
		t.Fatalf("id_prefix=sim 应得 sim_1，body=%s", body)
	}
}

func TestGetDeviceAfterRemovedFromLive404(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_gone")
	code, body := e.del(t, "/devices/sim_gone")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	code, body, _ = e.get(t, "/devices/sim_gone")
	if code != http.StatusNotFound {
		t.Fatalf("摘 live 后 GET /devices/{id} 应 404，得到 %d body=%s", code, body)
	}
}

func TestPostTemplateForbidsDeviceIDAndWriteQueue(t *testing.T) {
	e := newEnv(t)
	dev := e.deviceBody("sim_in_tmpl")
	code, body := e.postTemplate(t, "bad_id", dev)
	if code != http.StatusBadRequest {
		t.Fatalf("模板含 device_id 应 400，得到 %d body=%s", code, body)
	}
	delete(dev, "device_id")
	beh, _ := dev["behavior"].(map[string]any)
	beh["write_queue_depth"] = 256
	beh["write_drain_timeout_sec"] = 2
	code, body = e.postTemplate(t, "bad_wq", dev)
	if code != http.StatusBadRequest {
		t.Fatalf("模板含 write_queue_* 应 400，得到 %d body=%s", code, body)
	}
}

func TestTemplatePersistedUnderConfigsTemplates(t *testing.T) {
	e := newEnv(t)
	dev := e.deviceBody("ignored")
	delete(dev, "device_id")
	code, body := e.postTemplate(t, "default_a3", dev)
	if code != http.StatusCreated {
		t.Fatalf("POST /templates 应 201，得到 %d body=%s", code, body)
	}
	if strField(decodeMap(t, body), "template_id") != "default_a3" {
		t.Fatalf("应回 template_id，body=%s", body)
	}
	p := filepath.Join(e.templates, "default_a3.yaml")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("模板应落盘 %s: %v", p, err)
	}
}

func TestTemplatePathTraversalRejected(t *testing.T) {
	e := newEnv(t)
	secret := filepath.Join(filepath.Dir(e.templates), "secret.yaml")
	if err := os.WriteFile(secret, []byte("device:\n  device_id: leaked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/templates/..%2fsecret", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("GET 模板路径穿越不得 200，body=%s", body)
	}
	req, err = http.NewRequest(http.MethodDelete, e.srv.URL+"/templates/..%2fsecret", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		if _, err := os.Stat(secret); err == nil {
			t.Fatal("DELETE 路径穿越不得删掉模板目录外的 yaml")
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("secret.yaml 应仍在盘上: %v", err)
	}
}

func TestListDevicesIncludesEnterpriseAndType(t *testing.T) {
	e := newEnv(t)
	// Phase 11：三级是挂靠后才有的运行态，没 start 的设备这三项是空的。
	e.createDevice(t, "sim_flt_1")
	e.startDevice(t, "sim_flt_1")
	code, body, _ := e.get(t, "/devices")
	if code != http.StatusOK {
		t.Fatalf("GET /devices 应 200，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	devs, _ := m["devices"].([]any)
	if len(devs) == 0 {
		t.Fatal("应列出设备")
	}
	row, _ := devs[0].(map[string]any)
	if strField(row, "device_id") != "sim_flt_1" {
		t.Fatalf("device_id=%s body=%s", strField(row, "device_id"), body)
	}
	if strField(row, "enterprise") != "demo" || strField(row, "device_type") != "A3" {
		t.Fatalf("列表应含 enterprise/device_type，body=%s", body)
	}
}
