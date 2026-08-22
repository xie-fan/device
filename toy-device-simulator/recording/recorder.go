package recording

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"toy-device-simulator/protocol"
)

type FrameRow struct {
	TS            string `json:"ts"`
	Direction     string `json:"direction"`
	FirstByte     string `json:"first_byte"`
	Topic         string `json:"topic"`
	Stage         uint32 `json:"stage"`
	Seq           uint32 `json:"seq"`
	UUID          uint32 `json:"uuid"`
	NeedAck       uint32 `json:"need_ack"`
	PayloadLen    int    `json:"payload_len"`
	HeaderLen     int    `json:"header_len"`
	PayloadSHA256 string `json:"payload_sha256"`
	InjectedFault string `json:"injected_fault"`
	Text          string `json:"text,omitempty"`
}

type TurnRow struct {
	DeviceID        string `json:"device_id"`
	InstanceID      string `json:"instance_id"`
	TurnID          string `json:"turn_id"`
	UplinkUUID      uint32 `json:"uplink_uuid"`
	InjectedFault   string `json:"injected_fault"`
	UplinkEndReason string `json:"uplink_end_reason"`
	TurnEndReason   string `json:"turn_end_reason"`
	ReplyKind       string `json:"reply_kind"`
	SeqBefore       int    `json:"seq_before"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at"`
}

type job struct {
	kind  string // frame / pcm / turn
	path  string
	line  []byte
	pcm   []byte
	mkdir string
}

type Recorder struct {
	enableFrames bool
	saveUp       bool
	saveDown     bool
	jobs         chan job
	wg           sync.WaitGroup
	stopped      atomic.Bool
	beforeWrite  atomic.Value // func()
}

func New(enableFrames, saveUp, saveDown bool) *Recorder {
	r := &Recorder{
		enableFrames: enableFrames,
		saveUp:       saveUp,
		saveDown:     saveDown,
		jobs:         make(chan job, 256),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

func (r *Recorder) SetBeforeWrite(fn func()) {
	r.beforeWrite.Store(fn)
}

func (r *Recorder) Stop() {
	if r == nil || !r.stopped.CompareAndSwap(false, true) {
		return
	}
	close(r.jobs)
	r.wg.Wait()
}

func (r *Recorder) submit(j job) {
	if r == nil || r.stopped.Load() {
		return
	}
	select {
	case r.jobs <- j:
	default:
		go func() { r.jobs <- j }()
	}
}

func (r *Recorder) SubmitFrame(path string, row FrameRow) {
	if r == nil || !r.enableFrames {
		return
	}
	b, err := json.Marshal(row)
	if err != nil {
		return
	}
	r.submit(job{kind: "frame", path: path, line: append(b, '\n'), mkdir: filepath.Dir(path)})
}

func (r *Recorder) SubmitPCM(path string, pcm []byte, uplink bool) {
	if r == nil || len(pcm) == 0 {
		return
	}
	if uplink && !r.saveUp {
		return
	}
	if !uplink && !r.saveDown {
		return
	}
	r.submit(job{kind: "pcm", path: path, pcm: append([]byte(nil), pcm...), mkdir: filepath.Dir(path)})
}

func (r *Recorder) SubmitTurn(path string, row TurnRow) {
	if r == nil {
		return
	}
	b, err := json.Marshal(row)
	if err != nil {
		return
	}
	r.submit(job{kind: "turn", path: path, line: append(b, '\n'), mkdir: filepath.Dir(path)})
}

func (r *Recorder) loop() {
	defer r.wg.Done()
	for j := range r.jobs {
		if fn, _ := r.beforeWrite.Load().(func()); fn != nil {
			fn()
		}
		if j.mkdir != "" {
			_ = os.MkdirAll(j.mkdir, 0o755)
		}
		switch j.kind {
		case "frame", "turn":
			f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				continue
			}
			_, _ = f.Write(j.line)
			_ = f.Close()
		case "pcm":
			f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				continue
			}
			_, _ = f.Write(j.pcm)
			_ = f.Close()
		}
	}
}

func FormatTS(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func RowFromRaw(direction string, raw []byte, injected string, now time.Time) FrameRow {
	view := protocol.Inspect(raw)
	first := ""
	if len(raw) > 0 {
		first = string(raw[:1])
	}
	payload := view.Payload
	need := uint32(0)
	stage, seq, uuid := uint32(0), uint32(0), uint32(0)
	if view.OKHeader {
		need = view.Header.NeedAck
		stage = view.Header.Stage
		seq = view.Header.SequenceNumber
		uuid = view.Header.UUID
	}
	sum := sha256.Sum256(payload)
	text := ""
	if first == "{" || view.Topic != "" {
		text = string(payload)
		if len(text) > 512 {
			text = text[:512]
		}
	}
	return FrameRow{
		TS:            FormatTS(now),
		Direction:     direction,
		FirstByte:     first,
		Topic:         view.Topic,
		Stage:         stage,
		Seq:           seq,
		UUID:          uuid,
		NeedAck:       need,
		PayloadLen:    len(payload),
		HeaderLen:     view.HeaderLen,
		PayloadSHA256: hex.EncodeToString(sum[:]),
		InjectedFault: injected,
		Text:          text,
	}
}
