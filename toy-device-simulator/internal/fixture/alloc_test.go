package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllocFreshIDFormatAndIncrement(t *testing.T) {
	run := "11111111-1111-1111-1111-111111111111"
	a := Alloc(run, nil)
	if a.DeviceID != "sim_sr_"+run+"_1" || a.N != 1 {
		t.Fatalf("%+v", a)
	}
	b := Alloc(run, []Line{a})
	if b.N != 2 || b.DeviceID != "sim_sr_"+run+"_2" {
		t.Fatalf("%+v", b)
	}
	c := Alloc("other", []Line{a, b})
	if c.N != 1 {
		t.Fatal("不同 run-id 各自从 1 计")
	}
}

func TestAppendJSONLDoesNotOverwriteOtherRuns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh_ids.jsonl")
	if err := AppendJSONL(path, Alloc("runA", nil)); err != nil {
		t.Fatal(err)
	}
	exist, _ := ReadJSONL(path)
	if err := AppendJSONL(path, Alloc("runB", exist)); err != nil {
		t.Fatal(err)
	}
	all, err := ReadJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].RunID != "runA" || all[1].RunID != "runB" {
		t.Fatalf("%+v", all)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 {
		t.Fatal()
	}
}
