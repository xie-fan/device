package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

// POST /devices/{id}/trans：Ready 后发出 '3' 上行帧；下行 '3' 信封记 trans_response。
func TestTransEndpointSendsFrameAndResponseEvent(t *testing.T) {
	e := newEnv(t)
	dev := e.deviceBody("sim_tr")
	code, raw := e.post(t, "/devices", e.createBody(dev))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_tr")
	e.waitReady(t, "sim_tr", ins, gen)

	code, body := e.post(t, "/devices/sim_tr/trans", map[string]any{
		"event_type": "open_api",
		"path":       "/open/echo",
		"header":     map[string]any{"x-req": "1"},
		"body":       map[string]any{"k": "v"},
	})
	if code != http.StatusAccepted {
		t.Fatalf("trans 应 202，得到 %d body=%s", code, body)
	}

	var got protocol.TransferData
	found := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !found {
		for _, w := range e.conn("sim_tr").Writes() {
			if len(w) == 0 || w[0] != protocol.FirstTrans {
				continue
			}
			td, err := protocol.DecodeTransRequest(w)
			if err != nil {
				t.Fatalf("上行 '3' 帧无法解码: %v", err)
			}
			got, found = td, true
		}
		if !found {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("未写出 '3' 帧")
	}
	if got.DeviceID != "sim_tr" || got.Enterprise != "demo" || got.DeviceType != "A3" ||
		got.Request.Path != "/open/echo" || got.Request.Body["k"] != "v" {
		t.Fatalf("TransferData=%+v", got)
	}

	// 下行 '3' 信封 → trans_response 事件。
	var resp protocol.TransferData
	resp.Request.Path = "/open/echo"
	resp.Response.StatusCode = 200
	data, _ := json.Marshal(resp)
	env, _ := json.Marshal(protocol.Envelope{Topic: "demo/A3/sim_tr/trans/client", Data: data})
	e.conn("sim_tr").Push(append([]byte{protocol.FirstTrans}, env...))

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		code, body, _ := e.get(t, "/devices/sim_tr/events?instance_id="+ins)
		if code != http.StatusOK {
			t.Fatalf("events %d", code)
		}
		m := decodeMap(t, body)
		for _, evAny := range m["events"].([]any) {
			ev := evAny.(map[string]any)
			if strField(ev, "event_type") == "trans_response" {
				if reason := strField(ev, "reason"); !strings.Contains(reason, "path=/open/echo") {
					t.Fatalf("reason=%q", reason)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("未出现 trans_response 事件")
}

// 连上但未 Ready（服务端不回任何包）时 POST /trans 应 409；未启动应 404。
func TestTransNotReadyConflict(t *testing.T) {
	e := newEnv(t)
	e.auto.silent = true
	dev := e.deviceBody("sim_tr2")
	code, raw := e.post(t, "/devices", e.createBody(dev))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	code, body := e.post(t, "/devices/sim_tr2/trans", map[string]any{"path": "/open/echo"})
	if code != http.StatusNotFound {
		t.Fatalf("未启动应 404，得到 %d body=%s", code, body)
	}
	e.startDevice(t, "sim_tr2")
	code, body = e.post(t, "/devices/sim_tr2/trans", map[string]any{"path": "/open/echo"})
	if code != http.StatusConflict {
		t.Fatalf("未 Ready 应 409，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_tr2/trans", map[string]any{"event_type": "x"})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 path 应 400，得到 %d body=%s", code, body)
	}
}
