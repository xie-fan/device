package api

import (
	"net/http"
	"testing"

	"toy-device-simulator/manager"
)

func TestPostAssetWAVReturns201AndAssetID(t *testing.T) {
	e := newEnv(t)
	code, body := e.postAsset(t, "ok.wav", wavPCM(16000))
	if code != http.StatusCreated {
		t.Fatalf("POST /assets WAV 应 201，得到 %d body=%s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")
	if id == "" {
		t.Fatalf("201 必须返回非空 asset_id，body=%s", body)
	}
}

func TestPostAssetRejectsNonWAV400(t *testing.T) {
	e := newEnv(t)
	code, body := e.postAsset(t, "x.bin", []byte("not a wav"))
	if code != http.StatusBadRequest {
		t.Fatalf("非 WAV 应 400，得到 %d body=%s", code, body)
	}
}

func TestPostAssetExceedsMaxBytes400(t *testing.T) {
	e := newEnvCfg(t, func(c *manager.Config) { c.MaxAssetBytes = 16 })
	code, body := e.postAsset(t, "big.wav", wavPCM(16000))
	if code != http.StatusBadRequest {
		t.Fatalf("超过 max_asset_bytes 应 400，得到 %d body=%s", code, body)
	}
}

func TestDeleteAssetThenSpeakGets404WithoutCAS(t *testing.T) {
	e := newEnv(t)
	assetID := e.uploadWAV(t)
	code, body := e.del(t, "/assets/"+assetID)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE /assets 应 204，得到 %d body=%s", code, body)
	}
	e.createStartReady(t, "sim_del_ast")
	code, body = e.post(t, "/devices/sim_del_ast/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusNotFound {
		t.Fatalf("已删除 asset 的 speak 应 404 且不 CAS，得到 %d body=%s", code, body)
	}
	code, gbody, _ := e.get(t, "/devices/sim_del_ast")
	if code != http.StatusOK {
		t.Fatalf("设备仍应 live，GET %d body=%s", code, gbody)
	}
	turnsCode, turnsBody, _ := e.get(t, "/devices/sim_del_ast/turns?instance_id="+strField(decodeMap(t, gbody), "instance_id"))
	if turnsCode == http.StatusOK && containsBytes(turnsBody, "trn_") {
		t.Fatalf("404 路径不得占槽/留下 Turn，turns=%s", turnsBody)
	}
}
