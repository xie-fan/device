package recording

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

func TestRecorderWritesTurnBundleWithoutInstanceDir(t *testing.T) {
	dir := t.TempDir()
	r := New(true, true, true)
	defer r.Stop()
	turnDir := filepath.Join(dir, "sim_001", "turn_1")
	frames := filepath.Join(turnDir, "frames.jsonl")
	up := filepath.Join(turnDir, "uplink.pcm")
	turn := filepath.Join(turnDir, "turn.json")

	raw, _ := protocol.EncodeAudioFrame(protocol.NewPCMHeader(1, 0, 7, 4, 16000), []byte{1, 2, 3, 4})
	r.SubmitFrame(frames, RowFromRaw("outbound", raw, "bad_seq", time.Now()))
	r.SubmitPCM(up, []byte{1, 2, 3, 4}, true)
	r.SubmitTurn(turn, TurnRow{DeviceID: "sim_001", InstanceID: "ins_x", TurnID: "turn_1", TurnEndReason: "idle", ReplyKind: "tts"})
	r.Stop()

	if _, err := os.Stat(frames); err != nil {
		t.Fatal(err)
	}
	pcm, _ := os.ReadFile(up)
	if string(pcm[0:4]) == "RIFF" {
		t.Fatal("uplink.pcm 不得再包 RIFF")
	}
	if filepath.Base(filepath.Dir(frames)) != "turn_1" || filepath.Base(filepath.Dir(filepath.Dir(frames))) != "sim_001" {
		t.Fatalf("路径不得含 instance_id: %s", frames)
	}
}

func TestBadHeaderLenLessThan100(t *testing.T) {
	raw := append([]byte{protocol.FirstAudio}, make([]byte, 40)...)
	row := RowFromRaw("outbound", raw, "bad_header", time.Now())
	if row.HeaderLen >= 100 {
		t.Fatalf("header_len=%d", row.HeaderLen)
	}
	if row.FirstByte != "0" {
		t.Fatal(row.FirstByte)
	}
}

func TestRecorderStopConcurrentSubmit(t *testing.T) {
	dir := t.TempDir()
	r := New(true, true, true)
	path := filepath.Join(dir, "frames.jsonl")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.SubmitFrame(path, FrameRow{Direction: "outbound"})
			}
		}()
	}
	time.Sleep(2 * time.Millisecond)
	r.Stop()
	wg.Wait()
	r.Stop()
}
