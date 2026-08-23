package scenario

import (
	"encoding/json"
	"testing"
)

func TestScenarioAssertRequiresAfterEventSeq(t *testing.T) {
	s := Step{Action: "assert", DeviceID: "sim_1", EventType: "tts_done", TurnID: "trn_1"}
	if err := ValidateAssert(s); err == nil {
		t.Fatal("assert 必须带 after_event_seq")
	}
	s.AfterEventSeq = json.RawMessage("0")
	if err := ValidateAssert(s); err != nil {
		t.Fatalf("显式 after_event_seq=0 应合法: %v", err)
	}
}

func TestScenarioBatchStartWaitsReadyByDefault(t *testing.T) {
	var s Step
	if err := json.Unmarshal([]byte(`{"action":"batch_start","device_ids":["sim_1","sim_2"],"stagger_ms":50}`), &s); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(&s)
	if !BatchStartWaitsReady(s) {
		t.Fatal("batch_start 默认 wait_ready:true")
	}
}
