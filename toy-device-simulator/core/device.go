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
	Dial               DialFunc
	Fault              Fault
	RecorderHook       func()
	InstanceID         string
	EventLog           *EventLog
	EventLogMaxEntries int
	Phase2Recording    bool
	OnTurnTerminal     func(turnID string, ev Event)
	OnActivity         func()
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

	done chan Event
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
	turnDone     map[string]chan Event
	turnTerm     map[string]Event

	drainTimeout time.Duration
	sampleRate   uint32

	onTurnTerminal func(turnID string, ev Event)
	onActivity     func()
	deleted        bool

	phase2Recording  bool
	throttle         protocol.SleepThrottle
	speakableWaiters []chan SpeakableResult
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
	id := opts.InstanceID
	if id == "" {
		id = newInstanceID()
	}
	events := opts.EventLog
	if events == nil {
		events = NewEventLog(cfg.DeviceID, id)
		if opts.EventLogMaxEntries > 0 {
			events.SetMaxEntries(opts.EventLogMaxEntries)
		}
	}
	d := &DeviceInstance{
		cfg:             cfg,
		fault:           opts.Fault,
		instanceID:      id,
		dial:            opts.Dial,
		slot:            NewSlot(),
		events:          events,
		reports:         NewReportSeq(start),
		pendingMeta:     map[int]*pendingMeta{},
		outbound:        NewOutboundBuffer(cfg.Behavior.WriteQueueDepth),
		recorder:        recording.New(cfg.Recording.EnableFrameLog, cfg.Recording.SaveUplinkAudio, cfg.Recording.SaveDownlinkAudio),
		regDone:         make(chan error, 1),
		readyDone:       make(chan error, 1),
		finalizeDone:    make(chan struct{}),
		keepStop:        make(chan struct{}),
		drainTimeout:    time.Duration(cfg.Behavior.WriteDrainTimeoutSec) * time.Second,
		sampleRate:      uint32(cfg.Audio.SampleRate),
		phase2Recording: opts.Phase2Recording,
		onTurnTerminal:  opts.OnTurnTerminal,
		onActivity:      opts.OnActivity,
		turnDone:        map[string]chan Event{},
		turnTerm:        map[string]Event{},
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

func (d *DeviceInstance) Config() config.Device {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.cfg
}

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

func (d *DeviceInstance) SlotID() string {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.slot.ID()
}

// SlotInfo 供 HTTP interrupt / speak 短锁快照。
func (d *DeviceInstance) SlotInfo() (occupied bool, id string, finalizeStarted bool) {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.slot.Occupied(), d.slot.ID(), d.finalizeStarted
}

func (d *DeviceInstance) FinalizeStarted() bool {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.finalizeStarted
}

func (d *DeviceInstance) FinalizeCommitted() bool {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.finalizeCommitted
}

func (d *DeviceInstance) EventSeq() int {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.events.Seq()
}

func (d *DeviceInstance) EventLog() *EventLog { return d.events }

func (d *DeviceInstance) ThrottleLast() (int, bool) {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	return d.throttle.Last, d.throttle.Set
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
	_, end, uplink, kind := d.slot.Snapshot()
	turnID := d.slot.ID()
	ev, en := d.appendEventLocked("turn_terminal", turnID, "", end, uplink, kind)
	n.Event = ev
	n.EventWaiters = en.EventWaiters
	n.SlowSubs = en.SlowSubs
	n.wakes = en.wakes
	d.lastTerminal = ev
	d.turnTerm[turnID] = ev
	if d.turn != nil && d.turn.done != nil {
		n.done = d.turn.done
	} else {
		n.done = d.completionCh
	}
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
	if tn.Completion {
		ch := tn.done
		if ch != nil {
			select {
			case ch <- tn.Event:
			default:
			}
		}
		if d.onTurnTerminal != nil {
			d.onTurnTerminal(tn.Event.TurnID, tn.Event)
		}
	}
	eventNotifyOf(tn).NotifyHTTP()
	for _, n := range acc {
		n.NotifyHTTP()
	}
}

func (d *DeviceInstance) fireActivity() {
	fn := d.onActivity
	if fn == nil {
		return
	}
	go fn()
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
