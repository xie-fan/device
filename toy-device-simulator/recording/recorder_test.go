package recording

import (
	"bytes"
	"fmt"
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

// 队列满时关键任务（turn.json / events.jsonl）一条都不许丢——phase8 把
// 「盘就是真相源」写进了合同，静默丢事件直接违背它。帧和 PCM 可以丢。
// 不能改成阻塞：SubmitLine 的调用点在 EventLog 临界区里持着 device_mu。
func TestRecorderNeverDropsCriticalWhenQueueFull(t *testing.T) {
	dir := t.TempDir()
	r := New(true, true, true)
	events := filepath.Join(dir, "events.jsonl")
	frames := filepath.Join(dir, "frames.jsonl")

	// 把 worker 卡在第一条 job 上，好让 jobs（cap 256）填满。
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.SetBeforeWrite(func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	})
	r.SubmitFrame(frames, FrameRow{Direction: "outbound"})
	<-entered

	for i := 0; i < 600; i++ { // 远超 cap，逼出丢弃
		r.SubmitFrame(frames, FrameRow{Direction: "outbound"})
	}
	const want = 50
	for i := 0; i < want; i++ {
		r.SubmitLine(events, []byte(fmt.Sprintf("{\"seq\":%d}\n", i)))
	}
	close(release)
	r.Stop()

	b, err := os.ReadFile(events)
	if err != nil {
		t.Fatalf("关键事件文件都没有: %v", err)
	}
	if n := bytes.Count(b, []byte("\n")); n != want {
		t.Fatalf("关键事件必须一条不丢：想要 %d 行，实得 %d", want, n)
	}
	dropped, writeErrs := r.Stats()
	if dropped == 0 {
		t.Fatal("这个场景下 frame 本该有丢弃；dropped=0 说明队列压根没满，测试没测到东西")
	}
	if writeErrs != 0 {
		t.Fatalf("不该有落盘失败，得到 %d", writeErrs)
	}
}
