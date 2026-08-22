package core

import "errors"

type FrameKind int

const (
	KindAudioData FrameKind = iota // Stage 1/2、其它数据音频
	KindStage3
	KindACK
	KindReport
	KindManage
)

type Frame struct {
	Kind          FrameKind
	TurnID        string
	UUID          uint32
	Stage         uint32
	Seq           uint32
	Raw           []byte
	Topic         string
	InjectedFault string
	turn          *turnRuntime
}

type CancelResult int

const (
	CancelNone CancelResult = iota
	CancelEnqueued
	CancelBackpressure
)

type CloseResult int

const (
	CloseNone CloseResult = iota
	CloseEnqueued
	CloseBackpressure
)

var ErrDataFull = errors.New("outbound 数据满")

// OutboundBuffer 单一物理队列。len = 已入队尚未 Take 的帧数；正在 Write 的不计 len。
type OutboundBuffer struct {
	depth    int
	q        []Frame
	inFlight *Frame
	closing  bool
}

func NewOutboundBuffer(depth int) *OutboundBuffer {
	if depth < 2 {
		panic("write_queue_depth 必须 >= 2")
	}
	return &OutboundBuffer{depth: depth}
}

func (b *OutboundBuffer) Len() int { return len(b.q) }

func (b *OutboundBuffer) Closing() bool { return b.closing }

func (b *OutboundBuffer) Queued() []Frame {
	out := make([]Frame, len(b.q))
	copy(out, b.q)
	return out
}

var ErrClosing = errors.New("outbound 已 closing")

func (b *OutboundBuffer) EnqueueData(f Frame) error {
	if f.Kind == KindStage3 {
		return errors.New("Stage=3 不得走 EnqueueData")
	}
	if b.closing {
		return ErrClosing
	}
	if b.Len() >= b.depth-1 {
		return ErrDataFull
	}
	b.q = append(b.q, f)
	return nil
}

func (b *OutboundBuffer) TakeForWrite() (Frame, bool) {
	if len(b.q) == 0 {
		return Frame{}, false
	}
	f := b.q[0]
	b.q = b.q[1:]
	cp := f
	b.inFlight = &cp
	return f, true
}

func (b *OutboundBuffer) WriteDone() { b.inFlight = nil }

func (b *OutboundBuffer) CancelTurn(turnID string, uuid uint32, sendStage3 bool) CancelResult {
	kept := b.q[:0]
	for _, f := range b.q {
		if f.TurnID == turnID && (f.Kind == KindAudioData) && (f.Stage == 1 || f.Stage == 2) {
			continue
		}
		kept = append(kept, f)
	}
	b.q = kept
	if !sendStage3 {
		return CancelNone
	}
	return b.enqueueStage3(uuid, turnID)
}

func (b *OutboundBuffer) BeginClose(uuid uint32, turnID string, sendStage3 bool) CloseResult {
	b.closing = true
	keep := make([]Frame, 0, len(b.q))
	seen := map[uint32]bool{}
	for _, f := range b.q {
		if f.Kind == KindStage3 {
			if seen[f.UUID] {
				continue
			}
			seen[f.UUID] = true
			keep = append(keep, f)
		}
	}
	b.q = keep
	if !sendStage3 {
		return CloseNone
	}
	switch b.enqueueStage3(uuid, turnID) {
	case CancelNone:
		return CloseNone
	case CancelEnqueued:
		return CloseEnqueued
	default:
		return CloseBackpressure
	}
}

func (b *OutboundBuffer) enqueueStage3(uuid uint32, turnID string) CancelResult {
	for _, f := range b.q {
		if f.Kind == KindStage3 && f.UUID == uuid {
			return CancelEnqueued
		}
	}
	if b.Len() >= b.depth {
		return CancelBackpressure
	}
	b.q = append(b.q, Frame{Kind: KindStage3, UUID: uuid, TurnID: turnID, Stage: 3})
	return CancelEnqueued
}

// FillEmptyStage3 给尚未编码的 Stage=3 补上线上字节。调用方须已持 writePump_mu。
func (b *OutboundBuffer) FillEmptyStage3(encode func(uuid uint32, turnID string) []byte) {
	for i := range b.q {
		if b.q[i].Kind == KindStage3 && len(b.q[i].Raw) == 0 {
			b.q[i].Raw = encode(b.q[i].UUID, b.q[i].TurnID)
		}
	}
}

func (b *OutboundBuffer) CountQueuedAudio(turnID string) int {
	n := 0
	for _, f := range b.q {
		if f.TurnID == turnID && f.Kind == KindAudioData && (f.Stage == 1 || f.Stage == 2) {
			n++
		}
	}
	return n
}

func (b *OutboundBuffer) InFlight() *Frame { return b.inFlight }

func (b *OutboundBuffer) AttachMeta(fn func(*Frame)) {
	for i := range b.q {
		fn(&b.q[i])
	}
}
