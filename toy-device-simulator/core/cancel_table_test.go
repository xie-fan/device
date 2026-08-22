package core

import "testing"

func TestCancelTableStage3(t *testing.T) {
	if CancelSendsStage3(TurnReserved) || CancelSendsStage3(TurnTerminal) {
		t.Fatal("Reserved/Terminal 不发 Stage=3")
	}
	for _, st := range []TurnState{TurnSpeaking, TurnFinishingUpload, TurnWaitingReply} {
		if !CancelSendsStage3(st) {
			t.Fatalf("state=%d 应发 Stage=3", st)
		}
	}
}

func TestFinishingUploadDropsUnsentStage2(t *testing.T) {
	if !FinishingUploadDropsUnsentStage2(false) {
		t.Fatal("Stage=2 未发则不得再发")
	}
	if FinishingUploadDropsUnsentStage2(true) {
		t.Fatal("已发 Stage=2 后只补 Stage=3")
	}
}

func TestFailedJSONUsesCancelTurnNotBeginClose(t *testing.T) {
	d := DecideCompletion(CompletionInput{Phase: PhaseWaitingReply, FailedJSON: true})
	if d.Pump != PumpCancelTurn {
		t.Fatal("失败 JSON 必须 CancelTurn，连接保持")
	}
	d2 := DecideCompletion(CompletionInput{Phase: PhaseWaitingReply, ConnectionClose: true})
	if d2.Pump != PumpBeginClose {
		t.Fatal("进程退出才 BeginClose")
	}
}
