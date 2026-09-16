package api

import (
	"net/http"
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
