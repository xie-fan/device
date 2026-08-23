package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"toy-device-simulator/core"
	"toy-device-simulator/protocol"
)

type fakeConn struct {
	mu               sync.Mutex
	writes           [][]byte
	writeCh          chan []byte
	inbound          chan []byte
	closed           chan struct{}
	closeOnce        sync.Once
	closeCount       int
	hadStage2AtClose bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		writeCh: make(chan []byte, 1024),
		inbound: make(chan []byte, 64),
		closed:  make(chan struct{}),
	}
}

func (c *fakeConn) WriteMessage(_ int, data []byte) error {
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
	return nil
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	select {
	case m := <-c.inbound:
		return 1, m, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func (c *fakeConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.hadStage2AtClose = writesHaveStage(c.writes, protocol.StageFinished)
		c.closeCount++
		c.mu.Unlock()
		close(c.closed)
	})
	return nil
}

func (c *fakeConn) Push(msg []byte) { c.inbound <- append([]byte(nil), msg...) }

func (c *fakeConn) Writes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	copy(out, c.writes)
	return out
}

func (c *fakeConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

func (c *fakeConn) HadStage2AtClose() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hadStage2AtClose
}

func startAuto(t *testing.T, c *fakeConn, replyTTS bool) {
	t.Helper()
	go func() {
		sentTTS := false
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
					if strings.HasSuffix(env.Topic, "/register/server") {
						ack, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", protocol.RegisterAck{Code: 0})
						c.Push(ack)
					}
					if strings.HasSuffix(env.Topic, "/report/server") {
						echo, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", json.RawMessage(env.Data))
						c.Push(echo)
					}
				case protocol.FirstAudio:
					view := protocol.Inspect(msg)
					if !view.OKHeader || view.Header.Stage != protocol.StageUploading {
						continue
					}
					if replyTTS && !sentTTS {
						sentTTS = true
						h := protocol.NewPCMHeader(protocol.StageUploading, 0, view.Header.UUID, 4, 16000)
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

func writesHaveStage(writes [][]byte, stage uint32) bool {
	for _, w := range writes {
		if len(w) <= protocol.HeaderBytes || w[0] != protocol.FirstAudio {
			continue
		}
		h, err := protocol.DecodeHeader(w[1:])
		if err != nil {
			continue
		}
		if h.Stage == stage {
			return true
		}
	}
	return false
}

func TestSpeakDefaultDoesNotBeginCloseBeforeStage2(t *testing.T) {
	root := findRoot(t)
	cfg := writeSpeakYAML(t)
	audio := filepath.Join(root, "testdata", "hello.wav")
	conn := newFakeConn()
	startAuto(t, conn, true)

	code, stdout := captureStdout(t, func() int {
		return run([]string{"--config", cfg, "--audio", audio}, core.Options{
			Dial: func(string, http.Header) (core.Conn, error) { return conn, nil },
		})
	})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	if !conn.HadStage2AtClose() {
		t.Fatal("默认路径不得在 Stage=2 写出前 BeginClose")
	}
	if !writesHaveStage(conn.Writes(), protocol.StageFinished) {
		t.Fatal("默认路径必须写出 Stage=2")
	}
	if conn.CloseCount() == 0 {
		t.Fatal("收口后应 Disconnected")
	}
	assertTerminalJSON(t, stdout)
}

func TestSpeakWaitPrintsTerminalThenDisconnect(t *testing.T) {
	root := findRoot(t)
	cfg := writeSpeakYAML(t)
	audio := filepath.Join(root, "testdata", "hello.wav")
	conn := newFakeConn()
	startAuto(t, conn, true)

	code, stdout := captureStdout(t, func() int {
		return run([]string{"--config", cfg, "--audio", audio, "--wait"}, core.Options{
			Dial: func(string, http.Header) (core.Conn, error) { return conn, nil },
		})
	})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	assertTerminalJSON(t, stdout)
	if !conn.HadStage2AtClose() {
		t.Fatal("--wait 不得在 Stage=2 写出前 BeginClose")
	}
	if conn.CloseCount() == 0 {
		t.Fatal("--wait 终态 JSON 之后应 Disconnected")
	}
}

func TestSpeakWaitFalseStillWaitTurn(t *testing.T) {
	root := findRoot(t)
	cfg := writeSpeakYAML(t)
	audio := filepath.Join(root, "testdata", "hello.wav")
	conn := newFakeConn()
	startAuto(t, conn, true)

	code, stdout := captureStdout(t, func() int {
		return run([]string{"--config", cfg, "--audio", audio, "--wait=false"}, core.Options{
			Dial: func(string, http.Header) (core.Conn, error) { return conn, nil },
		})
	})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	if !conn.HadStage2AtClose() {
		t.Fatal("--wait=false 仍须 WaitTurn，不得在 Stage=2 写出前 BeginClose")
	}
	assertTerminalJSON(t, stdout)
	if conn.CloseCount() == 0 {
		t.Fatal("应收口到 Disconnected")
	}
}

func assertTerminalJSON(t *testing.T, stdout string) {
	t.Helper()
	line := strings.TrimSpace(stdout)
	if line == "" {
		t.Fatal("预算内应打出终态 JSON")
	}
	var ev struct {
		Type string `json:"event_type"`
		End  string `json:"turn_end_reason"`
		Kind string `json:"reply_kind"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("终态 JSON 非法: %v raw=%q", err, stdout)
	}
	if ev.Type != "turn_terminal" {
		t.Fatalf("event_type=%s", ev.Type)
	}
	if ev.End != core.EndIdle || ev.Kind != core.ReplyTTS {
		t.Fatalf("终态 %+v", ev)
	}
}

func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := fn()
	_ = w.Close()
	os.Stdout = old
	b, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return code, string(b)
}

func TestSpeakRejectsEscapingDeviceID(t *testing.T) {
	root := findRoot(t)
	cfg, recDir := writeSpeakConfig(t)
	audio := filepath.Join(root, "testdata", "hello.wav")
	outside := filepath.Join(filepath.Dir(recDir), "etc")
	for _, id := range []string{"../etc", `..\etc`} {
		t.Run(id, func(t *testing.T) {
			dialed := false
			code := run([]string{"--config", cfg, "--audio", audio, "--device-id", id}, core.Options{
				Dial: func(string, http.Header) (core.Conn, error) {
					dialed = true
					return nil, net.ErrClosed
				},
			})
			if code == 0 {
				t.Fatalf("%q 应拒绝", id)
			}
			if dialed {
				t.Fatalf("%q 覆盖后应在 Dial 前被拒绝", id)
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("%q 不得在 output_dir 外 MkdirAll: %s (err=%v)", id, outside, err)
			}
		})
	}
}

func writeSpeakYAML(t *testing.T) string {
	p, _ := writeSpeakConfig(t)
	return p
}

func writeSpeakConfig(t *testing.T) (cfgPath, recDir string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "device.yaml")
	outDir := filepath.ToSlash(filepath.Join(dir, "rec"))
	raw := []byte(`
device:
  enterprise: "demo"
  device_type: "A3"
  device_id: "sim_001"
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1
  audio:
    format: "pcm"
    sample_rate: 16000
    channels: 1
    sample_format: "s16le"
    slice_ms: 100
    max_payload_size: 51200
  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 0
    keepalive_method: "report"
    report_sequence_start: 1
    report_echo_timeout_sec: 2
    register_ack_timeout_sec: 2
    first_reply_timeout_sec: 2
    downlink_idle_timeout_sec: 1
    non_audio_followup_sec: 1
    post_final_asr_silence_sec: 1
    wait_timeout_slack_sec: 1
    write_queue_depth: 256
    write_drain_timeout_sec: 1
    expect_downlink_need_ack: false
    downlink_ack: { mode: binary, sleep_ms: 0, code: 0 }
  uuid: { min: 1, max: 2147483647 }
  server: { url: "ws://127.0.0.1:8089/" }
  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "` + outDir + `"
`)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, filepath.Join(dir, "rec")
}

func findRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(wd) == "speak" {
		return filepath.Join(wd, "..", "..")
	}
	return wd
}
