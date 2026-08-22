package core

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/protocol"
	"toy-device-simulator/recording"
)

// Conn 设备连 core 的 WebSocket。读侧按 []byte 处理，禁止当 UTF-8 字符串。
type Conn interface {
	WriteMessage(messageType int, data []byte) error
	ReadMessage() (messageType int, p []byte, err error)
	SetWriteDeadline(t time.Time) error
	Close() error
}

type DialFunc func(url string, header http.Header) (Conn, error)

type Options struct {
	Dial         DialFunc
	Fault        Fault
	RecorderHook func()
}

type pendingMeta struct {
	kind  string
	once  Once
	timer *time.Timer
}

type turnRuntime struct {
	id        string
	uuid      uint32
	fault     Fault
	seqBefore int
	startedAt time.Time

	frozen   bool
	hasTTS   bool
	hasCmd   bool
	hasJSON  bool
	hasFinal bool
	hasInter bool

	early EarlyBuf

	firstReply *time.Timer
	ttsIdle    *time.Timer
	followup   *time.Timer
	silent     *time.Timer

	out       atomic.Int64
	drained   chan struct{}
	drainOnce sync.Once

	framesPath string
	upPath     string
	downPath   string
	turnPath   string
}

func (t *turnRuntime) signalDrained() {
	if t == nil {
		return
	}
	t.drainOnce.Do(func() { close(t.drained) })
}

type DeviceInstance struct {
	cfg        config.Device
	fault      Fault
	instanceID string
	dial       DialFunc

	deviceMu      sync.Mutex
	connMu        sync.Mutex
	reportMu      sync.Mutex
	writePumpMu   sync.Mutex
	writePumpCond *sync.Cond

	connState      ConnState
	conn           Conn
	connGeneration int
	outbound       *OutboundBuffer

	slot *Slot
	turn *turnRuntime

	events      *EventLog
	reports     *ReportSeq
	pendingMeta map[int]*pendingMeta

	recorder *recording.Recorder

	registerOnce    *Once
	registerTimer   *time.Timer
	registerAttempt int
	regDone         chan error
	readyDone       chan error

	finalizeStarted   bool
	finalizeCommitted bool
	finalizeUser      bool
	finalizeReason    string
	finalizeDone      chan struct{}

	readLoopDone chan struct{}
	keepStop     chan struct{}
	keepOnce     sync.Once

	completionCh chan Event
	lastTerminal Event

	drainTimeout time.Duration
	sampleRate   uint32

	speakPermitOnce atomic.Bool
}

func newInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("ins_%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func NewDevice(cfg config.Device, opts Options) *DeviceInstance {
	start := cfg.Behavior.ReportSequenceStart
	if start == 0 {
		start = 1
	}
	id := newInstanceID()
	d := &DeviceInstance{
		cfg:          cfg,
		fault:        opts.Fault,
		instanceID:   id,
		dial:         opts.Dial,
		slot:         NewSlot(),
		events:       NewEventLog(cfg.DeviceID, id),
		reports:      NewReportSeq(start),
		pendingMeta:  map[int]*pendingMeta{},
		outbound:     NewOutboundBuffer(cfg.Behavior.WriteQueueDepth),
		recorder:     recording.New(cfg.Recording.EnableFrameLog, cfg.Recording.SaveUplinkAudio, cfg.Recording.SaveDownlinkAudio),
		regDone:      make(chan error, 1),
		readyDone:    make(chan error, 1),
		finalizeDone: make(chan struct{}),
		keepStop:     make(chan struct{}),
		drainTimeout: time.Duration(cfg.Behavior.WriteDrainTimeoutSec) * time.Second,
		sampleRate:   uint32(cfg.Audio.SampleRate),
	}
	if d.drainTimeout <= 0 {
		d.drainTimeout = 2 * time.Second
	}
	d.writePumpCond = sync.NewCond(&d.writePumpMu)
	if d.dial == nil {
		d.dial = DialGorilla
	}
	if opts.RecorderHook != nil {
		d.recorder.SetBeforeWrite(opts.RecorderHook)
	}
	return d
}

func (d *DeviceInstance) InstanceID() string { return d.instanceID }

func (d *DeviceInstance) Config() config.Device { return d.cfg }

func (d *DeviceInstance) Fault() Fault { return d.fault }

func (d *DeviceInstance) ConnectionState() ConnState {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	d.connMu.Lock()
	defer d.connMu.Unlock()
	return d.connState
}

func (d *DeviceInstance) EventTypes() []string {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.events.Types()
}

func (d *DeviceInstance) Events() []Event {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.events.Snapshot()
}

func (d *DeviceInstance) LastTerminal() Event {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.lastTerminal
}

func (d *DeviceInstance) SlotOccupied() bool {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.slot.Occupied()
}

func (d *DeviceInstance) appendEventLocked(typ, turnID, reason, endReason, uplinkReason, replyKind string) (Event, EventNotify) {
	return d.events.AppendLocked(typ, turnID, reason, endReason, uplinkReason, replyKind)
}

func (d *DeviceInstance) terminalLocked(endReason, replyKind, uplinkIfEmpty string, phaseC bool) TerminalNotify {
	if d.finalizeStarted && !phaseC {
		return TerminalNotify{}
	}
	n := d.slot.TerminalLocked(endReason, replyKind, uplinkIfEmpty)
	if !n.Completion {
		return n
	}
	d.stopTurnTimersLocked()
	d.speakPermitOnce.CompareAndSwap(false, true) // Phase 1：释放 permit 为 once no-op
	_, end, uplink, kind := d.slot.Snapshot()
	turnID := d.slot.ID()
	ev, en := d.appendEventLocked("turn_terminal", turnID, "", end, uplink, kind)
	n.Event = ev
	n.EventWaiters = en.EventWaiters
	n.SlowSubs = en.SlowSubs
	d.lastTerminal = ev
	d.submitTurnFileLocked(ev)
	if d.turn != nil {
		d.turn.signalDrained()
	}
	return n
}

func (d *DeviceInstance) submitTurnFileLocked(ev Event) {
	if d.turn == nil {
		return
	}
	_, end, uplink, kind := d.slot.Snapshot()
	d.recorder.SubmitTurn(d.turn.turnPath, recording.TurnRow{
		DeviceID:        d.cfg.DeviceID,
		InstanceID:      d.instanceID,
		TurnID:          d.slot.ID(),
		UplinkUUID:      d.slot.UUID(),
		InjectedFault:   string(d.turn.fault),
		UplinkEndReason: uplink,
		TurnEndReason:   end,
		ReplyKind:       kind,
		SeqBefore:       d.turn.seqBefore,
		StartedAt:       recording.FormatTS(d.turn.startedAt),
		EndedAt:         recording.FormatTS(ev.At),
	})
}

func (d *DeviceInstance) stopTurnTimersLocked() {
	if d.turn == nil {
		return
	}
	stopTimer(d.turn.firstReply)
	stopTimer(d.turn.ttsIdle)
	stopTimer(d.turn.followup)
	stopTimer(d.turn.silent)
	d.turn.firstReply = nil
	d.turn.ttsIdle = nil
	d.turn.followup = nil
	d.turn.silent = nil
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

func (d *DeviceInstance) finishCritical(acc []EventNotify, tn TerminalNotify) {
	_ = acc // Phase 1：HTTP waiter / hub 恒空，仍必须累加不得丢
	if tn.Completion {
		ch := d.completionCh
		if ch != nil {
			select {
			case ch <- tn.Event:
			default:
			}
		}
	}
}

func (d *DeviceInstance) encodeStage3(uuid uint32, _ string) []byte {
	raw, err := protocol.EncodeAudioFrame(protocol.NewPCMHeader(protocol.StageBreak, 0, uuid, 0, d.sampleRate), nil)
	if err != nil {
		return []byte{protocol.FirstAudio}
	}
	return raw
}

func (d *DeviceInstance) allocUUIDLocked() uint32 {
	min, max := d.cfg.UUID.Min, d.cfg.UUID.Max
	if min < 1 {
		min = 1
	}
	if max > 2147483647 {
		max = 2147483647
	}
	if min > max {
		min, max = 1, 2147483647
	}
	span := uint64(max - min + 1)
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	n := binary.BigEndian.Uint64(buf[:]) % span
	return uint32(min) + uint32(n)
}

func seconds(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
