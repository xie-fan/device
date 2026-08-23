package api

import (
	"net/http"
	"testing"
)

func TestTurnsFramesAudioMissingInstanceID400(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_q")
	paths := []string{
		"/devices/sim_q/turns",
		"/devices/sim_q/turns/trn_1",
		"/devices/sim_q/turns/trn_1/frames",
		"/devices/sim_q/turns/trn_1/audio/uplink",
		"/devices/sim_q/turns/trn_1/audio/downlink",
		"/devices/sim_q/events",
	}
	for _, p := range paths {
		code, body, _ := e.get(t, p)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 缺 instance_id 应 400，得到 %d body=%s", p, code, body)
		}
	}
}

func TestTurnsDoNotSearchAcrossInstancesByTurnIDOnly(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, _ := e.createStartReady(t, "sim_cross")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_cross/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	if turnID == "" {
		t.Fatalf("turn_id 为空，body=%s", body)
	}
	code, body = e.del(t, "/devices/sim_cross")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	newIns := e.createDevice(t, "sim_cross")
	if newIns == oldIns {
		t.Fatal("重建必须分配新 instance_id")
	}
	listCode, listBody, _ := e.get(t, "/devices/sim_cross/turns?instance_id="+newIns)
	if listCode == http.StatusOK && containsBytes(listBody, turnID) {
		t.Fatalf("禁止只凭 turn_id 跨 instance 搜到旧 Turn，body=%s", listBody)
	}
}

func TestGetAudioWrapsPCMAsWAV(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	ins, _ := e.createStartReady(t, "sim_wav")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_wav/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	code, body, hdr := e.get(t, "/devices/sim_wav/turns/"+turnID+"/audio/uplink?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("GET uplink audio 应 200，得到 %d body=%s", code, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type 应为 audio/wav，得到 %q", ct)
	}
	if len(body) < 12 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WAVE" {
		t.Fatal("GET audio 应将 pcm 包成 WAV")
	}
}

func TestOldInstanceReadableWithinTTLAfterRecreate(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, _ := e.createStartReady(t, "sim_old")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_old/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	code, body = e.del(t, "/devices/sim_old")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	_ = e.createDevice(t, "sim_old")
	code, body, _ = e.get(t, "/devices/sim_old/turns/"+turnID+"?instance_id="+oldIns)
	if code != http.StatusOK {
		t.Fatalf("TTL 内旧 instance_id 应可读旧 Turn，得到 %d body=%s", code, body)
	}
}

func TestNewInstanceOldTurnID404(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	e.createStartReady(t, "sim_nt")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_nt/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	turnID := strField(decodeMap(t, body), "turn_id")
	code, body = e.del(t, "/devices/sim_nt")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	newIns := e.createDevice(t, "sim_nt")
	code, body, _ = e.get(t, "/devices/sim_nt/turns/"+turnID+"?instance_id="+newIns)
	if code != http.StatusNotFound {
		t.Fatalf("新 instance_id + 旧 turn_id 应 404，得到 %d body=%s", code, body)
	}
}

func TestGetDevice404AfterDeleteButOldEvents200WithinTTL(t *testing.T) {
	e := newEnv(t)
	ins, _ := e.createStartReady(t, "sim_ev")
	code, body := e.del(t, "/devices/sim_ev")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	code, body, _ = e.get(t, "/devices/sim_ev")
	if code != http.StatusNotFound {
		t.Fatalf("删除后 GET /devices/{id} 应 404，得到 %d body=%s", code, body)
	}
	code, body, _ = e.get(t, "/devices/sim_ev/events?instance_id="+ins)
	if code != http.StatusOK {
		t.Fatalf("TTL 内旧 events 应 200，得到 %d body=%s", code, body)
	}
	if !containsBytes(body, "device_deleted") {
		t.Fatalf("冻结日志应含 device_deleted，body=%s", body)
	}
}


