package api

import (
	"net/http"
	"testing"
)

func TestScenarioRunReturns202RunID(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_1")
	e.createDevice(t, "sim_2")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/scenarios/run", map[string]any{
		"name": "batch-hello",
		"steps": []any{
			map[string]any{"action": "batch_start", "device_ids": []string{"sim_1", "sim_2"}, "stagger_ms": 50},
			map[string]any{"action": "speak", "device_id": "sim_1", "asset_id": assetID, "wait": true},
			map[string]any{
				"action":          "assert",
				"device_id":       "sim_1",
				"instance_id":     "$prev.instance_id",
				"event_type":      "tts_done",
				"turn_id":         "$prev.turn_id",
				"after_event_seq": "$prev.seq_before",
			},
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("POST /scenarios/run 应 202，得到 %d body=%s", code, body)
	}
	runID := strField(decodeMap(t, body), "run_id")
	if runID == "" {
		t.Fatalf("202 必须返回非空 run_id，body=%s", body)
	}
}
