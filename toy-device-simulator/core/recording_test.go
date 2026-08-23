package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase1RecordingPathHasNoInstanceID(t *testing.T) {
	frames, uplink, downlink, turn, err := RecordingPaths("./recordings", "sim_001", "turn_abc")
	if err != nil {
		t.Fatal(err)
	}
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

func TestRecordingDirRejectsPathEscape(t *testing.T) {
	out := filepath.Join(t.TempDir(), "recordings")
	ids := []string{"../etc", `..\etc`, "..", "/etc", `C:\Windows`, "foo/bar", "foo\\bar", "a\x00b", ""}
	for _, id := range ids {
		if _, err := RecordingDir(out, id, "turn_1"); err == nil {
			t.Errorf("device_id %q 应拒绝", id)
		}
		if _, err := RecordingDir(out, "sim_001", id); err == nil {
			t.Errorf("turn_id %q 应拒绝", id)
		}
	}
	dir, err := RecordingDir("./recordings", "sim_001", "turn_abc")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Clean("./recordings")
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("合法路径应在 output_dir 下: %s", dir)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "etc")); !os.IsNotExist(err) {
		t.Fatalf("RecordingDir 不得在 output_dir 外创建目录: %v", err)
	}
}
