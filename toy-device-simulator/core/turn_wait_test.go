package core

import (
	"errors"
	"testing"
	"time"
)

func TestWaitTurnBoundToTurnIDAfterNextSpeak(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Behavior.DownlinkIdleTimeoutSec = 1
	d, _ := newTestDevice(t, cfg, FaultNone, autoOpts{replyTTS: true})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, err := DecodeWAV(testdataWAV(t))
	if err != nil {
		t.Fatal(err)
	}
	turnA, _, err := d.Speak(pcm.Samples)
	if err != nil {
		t.Fatal(err)
	}
	evA, err := d.WaitTurn(turnA, d.WaitBudgetFor(len(pcm.Samples)))
	if err != nil {
		t.Fatal(err)
	}
	if evA.TurnID != turnA {
		t.Fatalf("A 终态 turn_id=%s 得到 %s", turnA, evA.TurnID)
	}
	turnB, _, err := d.Speak(pcm.Samples)
	if err != nil {
		t.Fatal(err)
	}
	if turnB == turnA {
		t.Fatal("B 应是新 Turn")
	}
	evWaitA, err := d.WaitTurn(turnA, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if evWaitA.TurnID != turnA {
		t.Fatalf("Turn A 终态后立即 Speak B，WaitTurn(A) 仍须返回 A，得到 %s", evWaitA.TurnID)
	}
	if evWaitA.EventSeq != evA.EventSeq {
		t.Fatalf("须是 A 的终态 seq=%d 得到 %d", evA.EventSeq, evWaitA.EventSeq)
	}
}

func TestSpeakPermitFailDoesNotOccupy(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, _ := newTestDevice(t, cfg, FaultSkipRegister, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, err := DecodeWAV(testdataWAV(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = d.SpeakPermit(pcm.Samples, func() bool { return false })
	if !errors.Is(err, ErrSpeakPermit) {
		t.Fatalf("permit 失败应 ErrSpeakPermit，得到 %v", err)
	}
	if d.SlotOccupied() {
		t.Fatal("permit 失败不得留下占用槽")
	}
}
