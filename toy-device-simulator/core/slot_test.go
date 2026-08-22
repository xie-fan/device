package core

import "testing"

func TestUplinkEndReasonIsImmutable(t *testing.T) {
	s := NewSlot()
	_ = s.Occupy("t", 1, []byte{1})
	s.SetUplinkEnd("vad")
	s.SetUplinkEnd("stage2")
	if s.UplinkEnd() != "vad" {
		t.Fatalf("先写不改, 得到 %s", s.UplinkEnd())
	}
}

func TestTerminalLockedDoesNotWakeUnderLock(t *testing.T) {
	s := NewSlot()
	_ = s.Occupy("t", 1, []byte{1})
	s.SetState(TurnWaitingReply)
	s.held = true
	n := s.TerminalLocked(EndIdle, ReplyTTS, "stage2")
	if s.woke {
		t.Fatal("terminalLocked 不得在锁内唤醒")
	}
	s.held = false
	s.NotifyCompletion(n)
	st, end, uplink, kind := s.Snapshot()
	if st != TurnTerminal || end != EndIdle || uplink != "stage2" || kind != ReplyTTS {
		t.Fatalf("%v %s %s %s", st, end, uplink, kind)
	}
	if s.Occupied() {
		t.Fatal("Terminal 后应释槽")
	}
}

func TestTerminalLockedIsIdempotent(t *testing.T) {
	s := NewSlot()
	_ = s.Occupy("t", 1, []byte{1})
	n1 := s.TerminalLocked(EndIdle, ReplyTTS, "stage2")
	n2 := s.TerminalLocked(EndError, ReplyEmpty, "error")
	if !n1.Completion || n2.Completion {
		t.Fatal("第二次不得再写 turn_terminal")
	}
	_, end, _, _ := s.Snapshot()
	if end != EndIdle {
		t.Fatalf("end=%s", end)
	}
}

func TestStage4WritesVadFirst(t *testing.T) {
	s := NewSlot()
	_ = s.Occupy("t", 1, []byte{1})
	s.SetState(TurnSpeaking)
	s.SetUplinkEnd("vad")
	if s.UplinkEnd() != "vad" {
		t.Fatal()
	}
	st, _, _, _ := s.Snapshot()
	if st == TurnTerminal {
		t.Fatal("WaitingReply 前 Stage=4 只记 vad、补 Stage=2，不 Terminal")
	}
}
