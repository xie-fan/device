package core

import "time"

const (
	ReplyTTS        = "tts"
	ReplyCommand    = "command"
	ReplyJSON       = "json"
	ReplySilent     = "silent"
	ReplyCommandTTS = "command+tts"
	ReplyJSONTTS    = "json+tts"
	ReplyEmpty      = ""

	EndIdle           = "idle"
	EndTimeout        = "timeout"
	EndError          = "error"
	EndInterrupt      = "interrupt"
	EndConnectionLost = "connection_lost"

	PumpNone       = "none"
	PumpCancelTurn = "cancel_turn"
	PumpBeginClose = "begin_close"

	EventTTSDone     = "tts_done"
	EventProtocolErr = "protocol_error"
	EventDrop        = "expected_server_drop"
)

type Phase int

const (
	PhaseBeforeWaiting Phase = iota
	PhaseWaitingReply
	PhaseFinalizeStarted
)

type CompletionInput struct {
	Phase             Phase
	CallerIsPhaseC    bool
	HasTTS            bool
	HasCommand        bool
	HasSuccessJSON    bool
	HasIsFinal        bool
	HasInterim        bool
	FailedJSON        bool
	Interrupt         bool
	ConnectionClose   bool
	FirstReplyExpired bool
	TTSIdleExpired    bool
	FollowupExpired   bool
	SilentExpired     bool
	FaultDropRow      bool
}

type Decision struct {
	Terminal   bool
	ReplyKind  string
	EndReason  string
	ExtraEvent string
	Pump       string
}

func replyKind(in CompletionInput) string {
	switch {
	case in.HasCommand && in.HasTTS:
		return ReplyCommandTTS
	case in.HasSuccessJSON && in.HasTTS:
		return ReplyJSONTTS
	case in.HasTTS:
		return ReplyTTS
	case in.HasCommand:
		return ReplyCommand
	case in.HasSuccessJSON:
		return ReplyJSON
	default:
		return ReplyEmpty
	}
}

// DecideCompletion 编码 phase1.md §6 终止表。WaitingReply 之前除失败 JSON 外不得 Terminal。
func DecideCompletion(in CompletionInput) Decision {
	if in.Phase == PhaseFinalizeStarted && !in.CallerIsPhaseC {
		return Decision{}
	}
	if in.FailedJSON {
		return Decision{Terminal: true, EndReason: EndError, ExtraEvent: EventProtocolErr, Pump: PumpCancelTurn}
	}
	if in.Interrupt {
		return Decision{Terminal: true, ReplyKind: replyKind(in), EndReason: EndInterrupt, Pump: PumpCancelTurn}
	}
	if in.ConnectionClose {
		pump := PumpBeginClose
		if in.CallerIsPhaseC {
			pump = PumpBeginClose
		}
		return Decision{Terminal: true, ReplyKind: replyKind(in), EndReason: EndConnectionLost, Pump: pump}
	}
	if in.Phase != PhaseWaitingReply {
		return Decision{}
	}
	kind := replyKind(in)
	if in.TTSIdleExpired && in.HasTTS {
		return Decision{Terminal: true, ReplyKind: kind, EndReason: EndIdle, ExtraEvent: EventTTSDone, Pump: PumpNone}
	}
	if in.FollowupExpired && (in.HasCommand || in.HasSuccessJSON) && !in.HasTTS {
		return Decision{Terminal: true, ReplyKind: kind, EndReason: EndIdle, Pump: PumpNone}
	}
	if in.SilentExpired && in.HasIsFinal && kind == ReplyEmpty {
		return Decision{Terminal: true, ReplyKind: ReplySilent, EndReason: EndIdle, Pump: PumpNone}
	}
	if in.FirstReplyExpired && kind == ReplyEmpty && !in.HasIsFinal {
		extra := ""
		if in.FaultDropRow {
			extra = EventDrop
		}
		return Decision{Terminal: true, ReplyKind: ReplyEmpty, EndReason: EndTimeout, ExtraEvent: extra, Pump: PumpNone}
	}
	return Decision{}
}

func CanStartSilentTimer(firstReplyExpired, hasIsFinal, hasTerminalReply bool) bool {
	return firstReplyExpired && hasIsFinal && !hasTerminalReply
}

func WaitBudget(upload, firstReply, idle, followup, silent, slack time.Duration) time.Duration {
	max := idle
	if followup > max {
		max = followup
	}
	if silent > max {
		max = silent
	}
	return upload + firstReply + max + slack
}

func UploadDuration(pcmBytes, sampleRate, channels, bytesPerSample, sliceMs int) time.Duration {
	if sliceMs <= 0 || sampleRate <= 0 || channels <= 0 || bytesPerSample <= 0 {
		return 0
	}
	bytesPerSlice := sampleRate * channels * bytesPerSample * sliceMs / 1000
	if bytesPerSlice <= 0 {
		return 0
	}
	n := (pcmBytes + bytesPerSlice - 1) / bytesPerSlice
	if n == 0 {
		n = 1
	}
	return time.Duration(n*sliceMs) * time.Millisecond
}
