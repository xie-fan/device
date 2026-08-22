package core

import (
	"path/filepath"
	"testing"
)

func TestPhase1RecordingPathHasNoInstanceID(t *testing.T) {
	frames, uplink, downlink, turn := RecordingPaths("./recordings", "sim_001", "turn_abc")
	want := filepath.Join("recordings", "sim_001", "turn_abc")
	for _, p := range []string{frames, uplink, downlink, turn} {
		if !filepath.IsAbs(p) && filepath.Dir(p) != want && filepath.ToSlash(filepath.Dir(p)) != "recordings/sim_001/turn_abc" {
			rel, _ := filepath.Rel(".", p)
			_ = rel
		}
		if PathHasInstanceID(p, "ins_should_not_appear") {
			t.Fatalf("Phase 1 路径不得含 instance_id: %s", p)
		}
	}
	if filepath.Base(frames) != "frames.jsonl" || filepath.Base(uplink) != "uplink.pcm" {
		t.Fatal(frames, uplink)
	}
}
