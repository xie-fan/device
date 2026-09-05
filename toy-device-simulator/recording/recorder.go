package recording

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	// Phase 5e：下行实际格式（首个 TTS 帧头），回放 API 据此处理；空=无下行音频。
	DownFormat     string `json:"down_format,omitempty"`
	DownSampleRate int    `json:"down_sample_rate,omitempty"`
	// Phase 9：下行 TTS 帧 payload_len 之和。老录音没有这个字段，读回来是 0。
	DownBytes int `json:"down_bytes,omitempty"`
	// Phase 8：上行的设备线上格式。回放上行原本靠「设备现在的配置」，
	// 跨重启回看历史时那份配置可能已经改过、设备甚至已删除，所以记进 turn.json。
	UpFormat     string `json:"up_format,omitempty"`
	UpSampleRate int    `json:"up_sample_rate,omitempty"`
	UpChannels   int    `json:"up_channels,omitempty"`
}

// 任务分两级：关键任务丢了就说不出「这一轮发生了什么」，辅助材料丢了不影响判定。
const (
	kindFrame    = "frame"
	kindPCM      = "pcm"
	kindCritical = "turn" // turn.json 与 events.jsonl
)

type job struct {
	kind  string // kindFrame / kindPCM / kindCritical
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
	// overflow 是关键任务（turn.json / events.jsonl）在 jobs 满时的暂存区，不设上限。
	// 只有 jobs 满时才会有东西进来，而那说明 worker 正在忙着排——它每轮都会顺手排干这里。
	overflow    []job
	dropped     atomic.Int64 // 丢掉的 frame/pcm 条数
	writeErrors atomic.Int64 // 落盘失败次数
	wg          sync.WaitGroup
	mu          sync.Mutex
	stopped     atomic.Bool
	beforeWrite atomic.Value // func()
}

// Stats 供调用方判断「历史不全」到底是设备没发、还是 recorder 丢了/写失败了。
func (r *Recorder) Stats() (dropped, writeErrors int64) {
	if r == nil {
		return 0, 0
	}
	return r.dropped.Load(), r.writeErrors.Load()
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
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.stopped.CompareAndSwap(false, true) {
		r.mu.Unlock()
		return
	}
	close(r.jobs)
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Recorder) submit(j job) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Load() {
		return
	}
	select {
	case r.jobs <- j:
		return
	default:
	}
	// 队列满。这里禁止阻塞：SubmitLine 的调用点在 EventLog 的临界区里（持 device_mu），
	// 一阻塞就把整台设备卡住；也禁止另起 goroutine 补发，那会在 Stop 之后向已关闭的
	// channel 发送。
	if j.kind == kindCritical {
		// turn.json 与 events.jsonl 是「盘就是真相源」的那份真相（phase8），丢了
		// 就说不出这一轮服务端回过什么。进暂存区，worker 每轮排干。
		r.overflow = append(r.overflow, j)
		return
	}
	// 帧与 PCM 是辅助材料，丢了不影响「这一轮发生了什么」的判定，可丢——但要出声，
	// 否则「录音不全」会被误读成设备行为异常。
	if n := r.dropped.Add(1); n == 1 || n%1000 == 0 {
		fmt.Fprintf(os.Stderr, "recorder: 队列满，已丢弃 %d 条 frame/pcm\n", n)
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
	r.submit(job{kind: kindFrame, path: path, line: append(b, '\n'), mkdir: filepath.Dir(path)})
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
	r.submit(job{kind: kindPCM, path: path, pcm: append([]byte(nil), pcm...), mkdir: filepath.Dir(path)})
}

// SubmitLine 追加一行已序列化的 NDJSON（Phase 8 事件落盘）。走同一条异步队列：
// 调用点在 EventLog 的临界区里，禁止同步 IO。
func (r *Recorder) SubmitLine(path string, line []byte) {
	if r == nil || len(line) == 0 {
		return
	}
	r.submit(job{kind: kindCritical, path: path, line: line, mkdir: filepath.Dir(path)})
}

func (r *Recorder) SubmitTurn(path string, row TurnRow) {
	if r == nil {
		return
	}
	b, err := json.Marshal(row)
	if err != nil {
		return
	}
	r.submit(job{kind: kindCritical, path: path, line: append(b, '\n'), mkdir: filepath.Dir(path)})
}

func (r *Recorder) loop() {
	defer r.wg.Done()
	for j := range r.jobs {
		r.drainOverflow()
		r.write(j)
	}
	// Stop 关了 channel 才走到这：暂存区里的关键任务还得写完。
	r.drainOverflow()
}

// drainOverflow 排干暂存区。只在 jobs 满时才会有内容，而那时 worker 必然还有
// 至少一条待处理的 job，所以每轮都会回到这里，不会出现「暂存区有货但没人排」。
func (r *Recorder) drainOverflow() {
	for {
		r.mu.Lock()
		if len(r.overflow) == 0 {
			r.mu.Unlock()
			return
		}
		j := r.overflow[0]
		r.overflow = r.overflow[1:]
		r.mu.Unlock()
		r.write(j)
	}
}

func (r *Recorder) write(j job) {
	if fn, _ := r.beforeWrite.Load().(func()); fn != nil {
		fn()
	}
	if j.mkdir != "" {
		if err := os.MkdirAll(j.mkdir, 0o755); err != nil {
			r.noteWriteErr(j.path, err)
			return
		}
	}
	data := j.line
	if j.kind == kindPCM {
		data = j.pcm
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.noteWriteErr(j.path, err)
		return
	}
	if _, err := f.Write(data); err != nil {
		r.noteWriteErr(j.path, err)
	}
	if err := f.Close(); err != nil {
		r.noteWriteErr(j.path, err)
	}
}

// noteWriteErr：磁盘满、路径不可写、杀毒软件锁文件都会走到这。原本是 `_, _ =`
// 全丢，于是「盘上历史缺了一段」和「设备本来就没发」长得一模一样。
func (r *Recorder) noteWriteErr(path string, err error) {
	if n := r.writeErrors.Add(1); n == 1 || n%100 == 0 {
		fmt.Fprintf(os.Stderr, "recorder: 落盘失败（累计 %d 次）%s: %v\n", n, path, err)
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
