package core

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/protocol"
)

type FakeConn struct {
	mu         sync.Mutex
	writes     [][]byte
	writeCh    chan []byte
	inbound    chan []byte
	closed     chan struct{}
	closeOnce  sync.Once
	closeCount int
	writeGate  chan struct{}
	header     http.Header
}

func NewFakeConn() *FakeConn {
	return &FakeConn{
		writeCh: make(chan []byte, 1024),
		inbound: make(chan []byte, 64),
		closed:  make(chan struct{}),
	}
}

func (c *FakeConn) WriteMessage(_ int, data []byte) error {
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	cp := append([]byte(nil), data...)
	c.mu.Lock()
	c.writes = append(c.writes, cp)
	c.mu.Unlock()
	select {
	case c.writeCh <- cp:
	default:
	}
	if c.writeGate != nil {
		select {
		case <-c.writeGate:
		case <-c.closed:
			return net.ErrClosed
		}
	}
	return nil
}

func (c *FakeConn) ReadMessage() (int, []byte, error) {
	select {
	case m := <-c.inbound:
		return 1, m, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *FakeConn) SetWriteDeadline(time.Time) error { return nil }

func (c *FakeConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closeCount++
		c.mu.Unlock()
		close(c.closed)
	})
	return nil
}

func (c *FakeConn) Push(msg []byte) { c.inbound <- append([]byte(nil), msg...) }

func (c *FakeConn) Writes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	copy(out, c.writes)
	return out
}

func (c *FakeConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

type autoOpts struct {
	replyTTS   bool
	failJSON   bool
	needAck    bool
	regCode    int
	dupRegAck  bool
	holdWrites bool
}

func startAutoCore(t *testing.T, c *FakeConn, opts autoOpts) {
	t.Helper()
	go func() {
		sentTTS := false
		sentFail := false
		for {
			select {
			case msg := <-c.writeCh:
				if len(msg) == 0 {
					continue
				}
				switch msg[0] {
				case protocol.FirstManage:
					env, err := protocol.DecodeManage(msg)
					if err != nil {
						continue
					}
					if topicEnds(env.Topic, "/register/server") {
						ack, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", protocol.RegisterAck{Code: opts.regCode})
						c.Push(ack)
						if opts.dupRegAck {
							c.Push(ack)
						}
					}
					if topicEnds(env.Topic, "/report/server") {
						echo, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", json.RawMessage(env.Data))
						c.Push(echo)
					}
				case protocol.FirstAudio:
					view := protocol.Inspect(msg)
					if !view.OKHeader || view.Header.Stage != protocol.StageUploading {
						continue
					}
					if opts.failJSON && !sentFail {
						sentFail = true
						c.Push([]byte(`{"RequestID":"r1","Code":1,"CodeMsg":"音频处理失败，请稍后重试","Data":null}`))
						continue
					}
					if opts.replyTTS && !sentTTS {
						sentTTS = true
						h := protocol.NewPCMHeader(protocol.StageUploading, 0, view.Header.UUID, 4, 16000)
						if opts.needAck {
							h.NeedAck = 1
						}
						frame, _ := protocol.EncodeAudioFrame(h, []byte{0x01, 0x02, 0x03, 0x04})
						c.Push(frame)
					}
				}
			case <-c.closed:
				return
			}
		}
	}()
}

func testdataWAV(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	p := filepath.Join(filepath.Dir(file), "..", "testdata", "hello.wav")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testDeviceCfg(t *testing.T) config.Device {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	p := filepath.Join(filepath.Dir(file), "..", "configs", "example_device.yaml")
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Recording.OutputDir = t.TempDir()
	cfg.Behavior.RegisterAckTimeoutSec = 2
	cfg.Behavior.ReportEchoTimeoutSec = 2
	cfg.Behavior.FirstReplyTimeoutSec = 1
	cfg.Behavior.DownlinkIdleTimeoutSec = 1
	cfg.Behavior.NonAudioFollowupSec = 1
	cfg.Behavior.PostFinalASRSilenceSec = 1
	cfg.Behavior.WaitTimeoutSlackSec = 1
	cfg.Behavior.WriteDrainTimeoutSec = 1
	cfg.Behavior.KeepaliveIntervalSec = 0
	return cfg
}

func newTestDevice(t *testing.T, cfg config.Device, fault Fault, auto autoOpts) (*DeviceInstance, *FakeConn) {
	t.Helper()
	conn := NewFakeConn()
	if auto.holdWrites {
		conn.writeGate = make(chan struct{})
	}
	startAutoCore(t, conn, auto)
	d := NewDevice(cfg, Options{
		Fault: fault,
		Dial: func(_ string, h http.Header) (Conn, error) {
			conn.header = h.Clone()
			return conn, nil
		},
	})
	t.Cleanup(func() {
		if conn.writeGate != nil {
			close(conn.writeGate)
		}
		d.Shutdown()
	})
	return d, conn
}

func TestStartHandshakeHeaders(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := conn.header.Get("Device"); got != "demo/A3/sim_001" {
		t.Fatalf("Device=%s", got)
	}
	if conn.header.Get("Action") != "chatbot" {
		t.Fatal("Action 必须为 chatbot")
	}
}

func TestHappyPathRegisterReportTTS(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Behavior.DownlinkIdleTimeoutSec = 1
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{replyTTS: true, needAck: true})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d.ConnectionState() != ConnReady {
		t.Fatalf("state=%v", d.ConnectionState())
	}
	pcm, err := DecodeWAV(testdataWAV(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Speak(pcm.Samples); err != nil {
		t.Fatal(err)
	}
	ev, err := d.WaitTurn(d.WaitBudgetFor(len(pcm.Samples)))
	if err != nil {
		t.Fatal(err)
	}
	if ev.EndReason != EndIdle || ev.ReplyKind != ReplyTTS {
		t.Fatalf("%+v", ev)
	}
	types := d.EventTypes()
	want := []string{"connected", "registering", "registered", "reporting", "ready"}
	for _, w := range want {
		if !contains(types, w) {
			t.Fatalf("缺少事件 %s: %v", w, types)
		}
	}
	if !contains(types, "tts_chunk") || !contains(types, "tts_done") || !contains(types, "turn_terminal") {
		t.Fatalf("TTS 终态事件不全: %v", types)
	}
	acks := 0
	hasStage1, hasStage2 := false, false
	for _, w := range conn.Writes() {
		if len(w) == 0 {
			continue
		}
		if w[0] == protocol.FirstAck {
			acks++
		}
		if w[0] == protocol.FirstAudio {
			h, err := protocol.DecodeHeader(w[1:])
			if err != nil {
				continue
			}
			if h.Stage == 1 && h.SequenceNumber == 0 {
				hasStage1 = true
			}
			if h.Stage == 2 {
				hasStage2 = true
			}
		}
	}
	if !hasStage1 || !hasStage2 {
		t.Fatal("上行必须含 Seq=0 的 Stage=1 与 Stage=2")
	}
	if acks != 1 {
		t.Fatalf("NeedAck TTS 应只 ACK 一次，得到 %d", acks)
	}
	d.Shutdown()
	if d.ConnectionState() != ConnDisconnected {
		t.Fatalf("收口后应为 Disconnected, got %v", d.ConnectionState())
	}
}

func TestFailedJSONDoesNotCloseSocket(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{failJSON: true})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, _ := DecodeWAV(testdataWAV(t))
	if _, _, err := d.Speak(pcm.Samples); err != nil {
		t.Fatal(err)
	}
	ev, err := d.WaitTurn(d.WaitBudgetFor(len(pcm.Samples)))
	if err != nil {
		t.Fatal(err)
	}
	if ev.EndReason != EndError {
		t.Fatalf("end=%s", ev.EndReason)
	}
	if conn.CloseCount() != 0 {
		t.Fatal("失败 JSON 不得自己关 socket")
	}
	if !contains(d.EventTypes(), "protocol_error") {
		t.Fatal("应有 protocol_error")
	}
	hasStage3 := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !hasStage3 {
		for _, w := range conn.Writes() {
			if len(w) > protocol.HeaderBytes && w[0] == protocol.FirstAudio {
				h, err := protocol.DecodeHeader(w[1:])
				if err == nil && h.Stage == protocol.StageBreak {
					hasStage3 = true
					break
				}
			}
		}
		if !hasStage3 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !hasStage3 {
		t.Fatal("失败 JSON 应按 CancelTurn 写出 Stage=3")
	}
}

func TestFailedJSONDuringUplinkNoStage1AfterStage3(t *testing.T) {
	// 多片 Stage=1 上行中打入失败 JSON；holdWrites 让出站队列可见。
	cfg := testDeviceCfg(t)
	cfg.Audio.SliceMs = 10
	d, conn := newTestDevice(t, cfg, FaultSkipRegister, autoOpts{holdWrites: true})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm := uplinkPCM(cfg, 24)
	_, uuid, err := d.Speak(pcm)
	if err != nil {
		t.Fatal(err)
	}
	waitQueuedAudio(t, d, 6, time.Second)

	failJSON := []byte(`{"RequestID":"r1","Code":1,"CodeMsg":"音频处理失败，请稍后重试","Data":null}`)
	waitGap(t, d, uuid, protocol.StageBreak, func() { conn.Push(failJSON) })

	releaseWriteGate(conn)
	ev, err := d.WaitTurn(d.WaitBudgetFor(len(pcm)))
	if err != nil {
		t.Fatal(err)
	}
	if ev.EndReason != EndError {
		t.Fatalf("end=%s", ev.EndReason)
	}

	stages := waitUUIDStages(t, conn, uuid, 2*time.Second, func(s []uint32) bool {
		return hasStage(s, protocol.StageBreak)
	})
	time.Sleep(30 * time.Millisecond)
	stages = audioStagesByUUID(conn.Writes(), uuid)
	assertNoStageAfterFirst(t, stages, protocol.StageBreak, protocol.StageUploading)
	assertNoStageAfterFirst(t, stages, protocol.StageBreak, protocol.StageFinished)
}

func TestVADDuringUplinkNoStage1AfterStage2(t *testing.T) {
	// 上行中 Push 匹配 UUID 的 Stage=4，补发 Stage=2 后不得再入队 Stage=1。
	cfg := testDeviceCfg(t)
	cfg.Audio.SliceMs = 10
	d, conn := newTestDevice(t, cfg, FaultSkipRegister, autoOpts{holdWrites: true})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm := uplinkPCM(cfg, 24)
	_, uuid, err := d.Speak(pcm)
	if err != nil {
		t.Fatal(err)
	}
	waitQueuedAudio(t, d, 6, time.Second)

	vad, err := protocol.EncodeAudioFrame(protocol.NewPCMHeader(protocol.StageVAD, 0, uuid, 0, uint32(cfg.Audio.SampleRate)), nil)
	if err != nil {
		t.Fatal(err)
	}
	waitGap(t, d, uuid, protocol.StageFinished, func() { conn.Push(vad) })

	releaseWriteGate(conn)
	stages := waitUUIDStages(t, conn, uuid, 2*time.Second, func(s []uint32) bool {
		return hasStage(s, protocol.StageFinished)
	})
	time.Sleep(30 * time.Millisecond)
	stages = audioStagesByUUID(conn.Writes(), uuid)
	assertNoStageAfterFirst(t, stages, protocol.StageFinished, protocol.StageUploading)
}

func TestBeginCloseKeepsQueuedStage3(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultSkipRegister, autoOpts{holdWrites: true})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d.ConnectionState() != ConnConnected {
		t.Fatalf("skip_register 应停在 Connected, got %v", d.ConnectionState())
	}
	pcm, _ := DecodeWAV(testdataWAV(t))
	if _, _, err := d.Speak(pcm.Samples); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d.writePumpMu.Lock()
		n := d.outbound.Len()
		inf := d.outbound.InFlight()
		d.writePumpMu.Unlock()
		if n > 0 || inf != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.deviceMu.Lock()
	acc, tn := d.applyFailedJSONLocked("test")
	d.deviceMu.Unlock()
	d.finishCritical(acc, tn)

	d.RequestFinalize("user_stop", true)
	d.writePumpMu.Lock()
	keep := 0
	for _, f := range d.outbound.Queued() {
		if f.Kind == KindStage3 {
			keep++
		}
	}
	inf := d.outbound.InFlight()
	d.writePumpMu.Unlock()
	if keep != 1 && (inf == nil || inf.Kind != KindStage3) {
		t.Fatalf("BeginClose 必须保留 Stage=3: queued=%d inFlight=%v", keep, inf)
	}
	close(conn.writeGate)
	conn.writeGate = nil
	d.WaitFinalize()
	if d.recorder != nil {
		d.recorder.Stop()
	}
}

func TestRegisterAckConsumedOnce(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, _ := newTestDevice(t, cfg, FaultNone, autoOpts{dupRegAck: true})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, typ := range d.EventTypes() {
		if typ == "registered" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("register ACK 只能消费一次, registered=%d", n)
	}
}

func TestSkipReportStaysRegistered(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultSkipReport, autoOpts{})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d.ConnectionState() != ConnRegistered {
		t.Fatalf("skip_report 应停在 Registered, got %v", d.ConnectionState())
	}
	for _, w := range conn.Writes() {
		if len(w) > 0 && w[0] == protocol.FirstManage {
			env, _ := protocol.DecodeManage(w)
			if topicEnds(env.Topic, "/report/server") {
				t.Fatal("skip_report 不得发送 report")
			}
		}
	}
	pcm, _ := DecodeWAV(testdataWAV(t))
	if _, _, err := d.Speak(pcm.Samples); err != nil {
		t.Fatal(err)
	}
}

func TestWAVMismatchDoesNotOccupy(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, _ := newTestDevice(t, cfg, FaultSkipRegister, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	bad := EncodeWAV(PCM{Samples: bytes.Repeat([]byte{0, 1}, 160), SampleRate: 8000, Channels: 1, BitsPerSample: 16})
	pcm, err := DecodeWAV(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := pcm.Match(cfg.Audio.SampleRate, cfg.Audio.Channels, cfg.Audio.SampleFormat); err == nil {
		t.Fatal("8kHz 应与 16k 配置不符")
	}
	if d.SlotOccupied() {
		t.Fatal("CAS 前失败不得占槽")
	}
}

func TestSlowRecorderDoesNotBlockRead(t *testing.T) {
	cfg := testDeviceCfg(t)
	conn := NewFakeConn()
	startAutoCore(t, conn, autoOpts{})
	block := make(chan struct{})
	d := NewDevice(cfg, Options{
		Fault: FaultSkipRegister,
		Dial: func(string, http.Header) (Conn, error) {
			return conn, nil
		},
		RecorderHook: func() { <-block },
	})
	t.Cleanup(func() {
		close(block)
		d.Shutdown()
	})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, _ := DecodeWAV(testdataWAV(t))
	if _, _, err := d.Speak(pcm.Samples); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 8; i++ {
		h := protocol.NewPCMHeader(1, uint32(i), 1, 2, 16000)
		frame, _ := protocol.EncodeAudioFrame(h, []byte{9, 9})
		conn.Push(frame)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("读循环被落盘阻塞")
	}
}

func TestBadUplinkFramesAreWritten(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultBadHeader, autoOpts{})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm, _ := DecodeWAV(testdataWAV(t))
	if _, _, err := d.Speak(pcm.Samples); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, w := range conn.Writes() {
			if len(w) > 0 && w[0] == protocol.FirstAudio && len(w)-1 < protocol.HeaderBytes {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bad_header 必须真正写出不足 100 字节的帧")
}

func TestWaitBudgetNotHardcoded(t *testing.T) {
	cfg := testDeviceCfg(t)
	d := NewDevice(cfg, Options{Fault: FaultSkipRegister, Dial: func(string, http.Header) (Conn, error) {
		return NewFakeConn(), nil
	}})
	t.Cleanup(d.Shutdown)
	got := d.WaitBudgetFor(3200)
	if got == 30*time.Second {
		t.Fatal("禁止写死 30s")
	}
}

func contains(ss []string, w string) bool {
	for _, s := range ss {
		if s == w {
			return true
		}
	}
	return false
}

func uplinkPCM(cfg config.Device, slices int) []byte {
	n := cfg.Audio.SampleRate * cfg.Audio.Channels * 2 * cfg.Audio.SliceMs / 1000
	if n <= 0 {
		n = 320
	}
	return make([]byte, n*slices)
}

func releaseWriteGate(conn *FakeConn) {
	if conn.writeGate != nil {
		close(conn.writeGate)
		conn.writeGate = nil
	}
}

func waitQueuedAudio(t *testing.T, d *DeviceInstance, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.writePumpMu.Lock()
		q := d.outbound.Len()
		d.writePumpMu.Unlock()
		if q >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待上行入队超时")
}

func outboundUUIDStages(d *DeviceInstance, uuid uint32) []uint32 {
	d.writePumpMu.Lock()
	defer d.writePumpMu.Unlock()
	var out []uint32
	if inf := d.outbound.InFlight(); inf != nil && inf.UUID == uuid {
		out = append(out, inf.Stage)
	}
	for _, f := range d.outbound.Queued() {
		if f.UUID == uuid && (f.Kind == KindAudioData || f.Kind == KindStage3) {
			out = append(out, f.Stage)
		}
	}
	return out
}

// waitGap 把注入点卡在「检查已通过、即将 enqueueData」：
// 若错误地先 Unlock，下行可先入队 Stage=2/3，随后的 Stage=1 就会排到后面；
// 若仍持 deviceMu，下行会被挡住，超时后继续入队，顺序保持正确。
func waitGap(t *testing.T, d *DeviceInstance, uuid, expectQueued uint32, inject func()) {
	t.Helper()
	var once sync.Once
	done := make(chan struct{})
	testBeforeUplinkEnqueue = func() {
		once.Do(func() {
			inject()
			deadline := time.Now().Add(80 * time.Millisecond)
			for time.Now().Before(deadline) {
				if hasStage(outboundUUIDStages(d, uuid), expectQueued) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			close(done)
		})
	}
	t.Cleanup(func() { testBeforeUplinkEnqueue = nil })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("未打到 Stage=1/2 入队前窗口")
	}
}

func audioStagesByUUID(writes [][]byte, uuid uint32) []uint32 {
	var out []uint32
	for _, w := range writes {
		if len(w) <= protocol.HeaderBytes || w[0] != protocol.FirstAudio {
			continue
		}
		h, err := protocol.DecodeHeader(w[1:])
		if err != nil {
			continue
		}
		if h.UUID == uuid {
			out = append(out, h.Stage)
		}
	}
	return out
}

func hasStage(stages []uint32, want uint32) bool {
	for _, s := range stages {
		if s == want {
			return true
		}
	}
	return false
}

func waitUUIDStages(t *testing.T, conn *FakeConn, uuid uint32, timeout time.Duration, pred func([]uint32) bool) []uint32 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var stages []uint32
	for time.Now().Before(deadline) {
		stages = audioStagesByUUID(conn.Writes(), uuid)
		if pred(stages) {
			return stages
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待写出序列超时: %v", stages)
	return stages
}

func assertNoStageAfterFirst(t *testing.T, stages []uint32, first, forbidden uint32) {
	t.Helper()
	seen := false
	for _, s := range stages {
		if s == first {
			seen = true
			continue
		}
		if seen && s == forbidden {
			t.Fatalf("第一个 Stage=%d 之后不得再出现 Stage=%d：%v", first, forbidden, stages)
		}
	}
	if !seen {
		t.Fatalf("未见到 Stage=%d：%v", first, stages)
	}
}
