package core

import (
	"testing"
	"time"
)

func TestCompletionMatrixRows(t *testing.T) {
	tests := []struct {
		name string
		in   CompletionInput
		want Decision
	}{
		{
			name: "仅 TTS idle",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasTTS: true, TTSIdleExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplyTTS, EndReason: EndIdle, ExtraEvent: EventTTSDone, Pump: PumpNone},
		},
		{
			name: "仅 command",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasCommand: true, FollowupExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplyCommand, EndReason: EndIdle, Pump: PumpNone},
		},
		{
			name: "仅成功 JSON",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasSuccessJSON: true, FollowupExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplyJSON, EndReason: EndIdle, Pump: PumpNone},
		},
		{
			name: "command+TTS",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasCommand: true, HasTTS: true, TTSIdleExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplyCommandTTS, EndReason: EndIdle, ExtraEvent: EventTTSDone, Pump: PumpNone},
		},
		{
			name: "JSON+TTS",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasSuccessJSON: true, HasTTS: true, TTSIdleExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplyJSONTTS, EndReason: EndIdle, ExtraEvent: EventTTSDone, Pump: PumpNone},
		},
		{
			name: "仅 IsFinal 无终态",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasIsFinal: true, SilentExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplySilent, EndReason: EndIdle, Pump: PumpNone},
		},
		{
			name: "仅 interim 正常 timeout 无 drop",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasInterim: true, FirstReplyExpired: true},
			want: Decision{Terminal: true, ReplyKind: ReplyEmpty, EndReason: EndTimeout, Pump: PumpNone},
		},
		{
			name: "fault drop 行 timeout",
			in:   CompletionInput{Phase: PhaseWaitingReply, FirstReplyExpired: true, FaultDropRow: true},
			want: Decision{Terminal: true, ReplyKind: ReplyEmpty, EndReason: EndTimeout, ExtraEvent: EventDrop, Pump: PumpNone},
		},
		{
			name: "失败 JSON",
			in:   CompletionInput{Phase: PhaseWaitingReply, FailedJSON: true, HasTTS: true},
			want: Decision{Terminal: true, EndReason: EndError, ExtraEvent: EventProtocolErr, Pump: PumpCancelTurn},
		},
		{
			name: "失败 JSON 在 WaitingReply 前也 Terminal",
			in:   CompletionInput{Phase: PhaseBeforeWaiting, FailedJSON: true},
			want: Decision{Terminal: true, EndReason: EndError, ExtraEvent: EventProtocolErr, Pump: PumpCancelTurn},
		},
		{
			name: "interrupt",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasTTS: true, Interrupt: true},
			want: Decision{Terminal: true, ReplyKind: ReplyTTS, EndReason: EndInterrupt, Pump: PumpCancelTurn},
		},
		{
			name: "连接收口",
			in:   CompletionInput{Phase: PhaseWaitingReply, HasCommand: true, ConnectionClose: true},
			want: Decision{Terminal: true, ReplyKind: ReplyCommand, EndReason: EndConnectionLost, Pump: PumpBeginClose},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideCompletion(tc.in)
			if got != tc.want {
				t.Fatalf("got=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestNoTerminalBeforeWaitingReplyExceptFailedJSON(t *testing.T) {
	in := CompletionInput{Phase: PhaseBeforeWaiting, HasTTS: true, HasCommand: true, TTSIdleExpired: true}
	got := DecideCompletion(in)
	if got.Terminal {
		t.Fatal("WaitingReply 前不得因 TTS/command 进入 Terminal")
	}
}

func TestFinalizeStartedBlocksNonPhaseC(t *testing.T) {
	in := CompletionInput{Phase: PhaseFinalizeStarted, HasTTS: true, TTSIdleExpired: true}
	if DecideCompletion(in).Terminal {
		t.Fatal("finalize_started 后完成矩阵不得 Terminal")
	}
	in.CallerIsPhaseC = true
	in.ConnectionClose = true
	got := DecideCompletion(in)
	if !got.Terminal || got.EndReason != EndConnectionLost {
		t.Fatalf("%+v", got)
	}
}

func TestInterimOnlyCannotStartSilentTimer(t *testing.T) {
	if CanStartSilentTimer(true, false, false) {
		t.Fatal("interim-only 禁止启动 post_final_asr_silence")
	}
	if !CanStartSilentTimer(true, true, false) {
		t.Fatal("first_reply 到期且 IsFinal 且无终态应能启动 silent")
	}
}

func TestWaitBudgetIsNotHardcoded30s(t *testing.T) {
	upload := 400 * time.Millisecond
	got := WaitBudget(upload, 20*time.Second, 20*time.Second, 5*time.Second, 5*time.Second, 5*time.Second)
	want := upload + 20*time.Second + 20*time.Second + 5*time.Second
	if got != want {
		t.Fatalf("got=%s want=%s", got, want)
	}
	if got == 30*time.Second {
		t.Fatal("禁止默认写死 30s")
	}
}

func TestUploadDurationFromPCMSlices(t *testing.T) {
	// 16kHz s16le mono 100ms = 3200 B；6400 B → 2 片 → 200ms
	got := UploadDuration(6400, 16000, 1, 2, 100)
	if got != 200*time.Millisecond {
		t.Fatalf("got=%s", got)
	}
}
