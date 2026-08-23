package core

import (
	"errors"
	"os"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

func TestInterruptEmptySlotIgnoresTurnID(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, _ := newTestDevice(t, cfg, FaultNone, autoOpts{})
	res, err := d.Interrupt("turn_whatever")
	if err != nil {
		t.Fatalf("槽空不得因 turn_id 失败: %v", err)
	}
	if res.Interrupted {
		t.Fatal("槽空应 interrupted=false")
	}
}

func TestInterruptTurnMismatchDoesNotCancel(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, _ := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, err := DecodeWAV(testdataWAV(t))
	if err != nil {
		t.Fatal(err)
	}
	turnID, _, err := d.Speak(pcm.Samples)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !d.SlotOccupied() {
		time.Sleep(5 * time.Millisecond)
	}
	_, err = d.Interrupt("turn_not_current")
	if !errors.Is(err, ErrTurnMismatch) {
		t.Fatalf("应 turn_mismatch，得到 %v", err)
	}
	if !d.SlotOccupied() || d.SlotID() != turnID {
		t.Fatal("turn_mismatch 不得 CancelTurn")
	}
}

func TestUplinkPCMMatchesSentNotDoubleWrite(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Recording.SaveUplinkAudio = true
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{replyTTS: true})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, err := DecodeWAV(testdataWAV(t))
	if err != nil {
		t.Fatal(err)
	}
	turnID, _, err := d.Speak(pcm.Samples)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.WaitTurn(turnID, d.WaitBudgetFor(len(pcm.Samples))); err != nil {
		t.Fatal(err)
	}
	d.Shutdown()
	_, up, _, _, err := RecordingPaths(cfg.Recording.OutputDir, cfg.DeviceID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(up)
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	for _, w := range conn.Writes() {
		if len(w) == 0 || w[0] != protocol.FirstAudio {
			continue
		}
		view := protocol.Inspect(w)
		if view.OKHeader && view.Header.Stage == protocol.StageUploading {
			sent += len(view.Payload)
		}
	}
	if sent == 0 {
		t.Fatal("应发出 Stage=1 PCM")
	}
	if len(got) != sent {
		t.Fatalf("uplink.pcm=%d 实际发出=%d 原始pcm=%d，不得双写约 2×", len(got), sent, len(pcm.Samples))
	}
}
