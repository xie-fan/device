package core

import "errors"

type TurnState int

const (
	TurnEmpty TurnState = iota
	TurnReserved
	TurnSpeaking
	TurnFinishingUpload
	TurnWaitingReply
	TurnTerminal
)

type Slot struct {
	state           TurnState
	id              string
	uuid            uint32
	pcm             []byte
	uplinkEndReason string
	turnEndReason   string
	replyKind       string
	woke            bool
	held            bool
}

func NewSlot() *Slot { return &Slot{} }

func (s *Slot) Occupied() bool {
	return s.state != TurnEmpty && s.state != TurnTerminal
}

func (s *Slot) PCM() []byte { return s.pcm }

func (s *Slot) ID() string       { return s.id }
func (s *Slot) UUID() uint32     { return s.uuid }
func (s *Slot) State() TurnState { return s.state }

func (s *Slot) Occupy(id string, uuid uint32, pcm []byte) error {
	if pcm == nil {
		return errors.New("必须先拷贝 PCM")
	}
	if s.Occupied() {
		return errors.New("槽已占用")
	}
	s.id = id
	s.uuid = uuid
	s.pcm = append([]byte(nil), pcm...)
	s.state = TurnReserved
	s.uplinkEndReason = ""
	s.turnEndReason = ""
	s.replyKind = ""
	s.woke = false
	s.held = false
	return nil
}

func (s *Slot) SetState(st TurnState) { s.state = st }

func (s *Slot) SetUplinkEnd(reason string) {
	if s.uplinkEndReason != "" {
		return
	}
	s.uplinkEndReason = reason
}

func (s *Slot) UplinkEnd() string { return s.uplinkEndReason }

type TerminalNotify struct {
	Completion   bool
	Event        Event
	EventWaiters int
	SlowSubs     int
}

// TerminalLocked 调用方必须已持锁。禁止在函数内唤醒。
func (s *Slot) TerminalLocked(endReason, replyKind, uplinkIfEmpty string) TerminalNotify {
	if s.state == TurnEmpty || s.state == TurnTerminal {
		return TerminalNotify{}
	}
	s.turnEndReason = endReason
	if s.replyKind == "" {
		s.replyKind = replyKind
	}
	if s.uplinkEndReason == "" {
		s.uplinkEndReason = uplinkIfEmpty
	}
	s.state = TurnTerminal
	s.pcm = nil
	return TerminalNotify{Completion: true}
}

func (s *Slot) NotifyCompletion(n TerminalNotify) {
	if s.held {
		s.woke = true
	}
	if n.Completion {
		s.woke = s.woke || !s.held && true
	}
}

func (s *Slot) Snapshot() (state TurnState, end, uplink, kind string) {
	return s.state, s.turnEndReason, s.uplinkEndReason, s.replyKind
}
