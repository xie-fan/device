package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withIO(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &out, io.Discard
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
	return &out
}

func TestHelpListsVerbs(t *testing.T) {
	var errb bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = io.Discard, &errb
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
	if code := simctl([]string{"--help"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	s := errb.String()
	for _, v := range []string{
		"up", "down", "status", "devices", "assets", "run", "turn", "audio", "history",
		"--asset", "--parallel", "--dirty", "--env", "--enterprise", "--device-type",
		"--side", "--instance", "--listen", "--config",
	} {
		if !strings.Contains(s, v) {
			t.Fatalf("help 缺 %q", v)
		}
	}
}

func TestFlagsFirst(t *testing.T) {
	got := flagsFirst([]string{"sim_1", "--asset", "ast_x", "--dirty"})
	want := []string{"--asset", "ast_x", "--dirty", "sim_1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
}

func TestSelectDevices(t *testing.T) {
	all := []deviceRow{
		{DeviceID: "a", Environment: "local", Enterprise: "vp", DeviceType: "spk"},
		{DeviceID: "b", Environment: "prod", Enterprise: "vp", DeviceType: "mic"},
	}
	got := selectDevices(all, "", "local", "vp", "")
	if len(got) != 1 || got[0].DeviceID != "a" {
		t.Fatalf("%v", got)
	}
	if n := selectDevices(all, "", "", "厂商全称不是简称", ""); len(n) != 0 {
		t.Fatalf("打错简称应选中 0 台, got %d", len(n))
	}
	if n := selectDevices(all, "a", "prod", "", ""); len(n) != 0 {
		t.Fatalf("id 与 env 合取应为空")
	}
}

func TestSummarizeFrames(t *testing.T) {
	raw := []byte(`{"direction":"outbound","payload_len":10}
{"direction":"inbound","payload_len":32}
{"direction":"inbound","payload_len":8}
`)
	st := summarizeFrames(raw)
	if st["lines"] != 3 || st["outbound"] != 1 || st["inbound"] != 2 || st["inbound_bytes"] != 40 {
		t.Fatalf("%v", st)
	}
}

func TestRunZeroDevices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devices" {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
	}))
	t.Cleanup(srv.Close)
	out := withIO(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	code := simctl([]string{"--listen", host, "run", "--asset", "ast_x"})
	if code == 0 {
		t.Fatalf("want 非 0, out=%s", out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte("选中 0 台")) {
		t.Fatalf("out=%s", out.Bytes())
	}
}

type runStub struct {
	resets, starts, waits, speaks int
	state                         string
	overridden                    bool
}

func (s *runStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []deviceRow{{
			DeviceID: "sim_1", InstanceID: "ins_1", InstanceState: s.state,
			ConnGeneration: 1, Environment: "local", Enterprise: "vp",
			DeviceType: "spk", Overridden: s.overridden,
		}}})
	})
	mux.HandleFunc("POST /devices/{id}/config/reset", func(w http.ResponseWriter, _ *http.Request) {
		s.resets++
		s.overridden = false
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"overridden":false}`))
	})
	mux.HandleFunc("POST /devices/{id}/start", func(w http.ResponseWriter, _ *http.Request) {
		s.starts++
		s.state = "running"
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"device_id":"sim_1","instance_id":"ins_1","conn_generation":2}`))
	})
	mux.HandleFunc("POST /devices/{id}/wait_ready", func(w http.ResponseWriter, _ *http.Request) {
		s.waits++
		_, _ = w.Write([]byte(`{"device_id":"sim_1","instance_id":"ins_1"}`))
	})
	mux.HandleFunc("POST /devices/{id}/speak_and_wait", func(w http.ResponseWriter, _ *http.Request) {
		s.speaks++
		_, _ = w.Write([]byte(`{"turn_id":"turn_1","instance_id":"ins_1","turn_end_reason":"idle","uplink_end_reason":"complete","reply_kind":"tts"}`))
	})
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("instance_id") == "" {
			http.Error(w, `{"error":"缺 instance_id"}`, 400)
			return
		}
		_, _ = w.Write([]byte(`{"turn_id":"turn_1","instance_id":"ins_1","turn_end_reason":"idle","uplink_end_reason":"complete","reply_kind":"tts","up_format":"pcm","down_format":"pcm","down_bytes":320}`))
	})
	return mux
}

func TestRunHappyAndDirty(t *testing.T) {
	stub := &runStub{state: "stopped", overridden: true}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	out := withIO(t)
	code := simctl([]string{"--listen", host, "run", "--asset", "ast_x"})
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if stub.resets != 1 || stub.starts != 1 || stub.waits != 1 || stub.speaks != 1 {
		t.Fatalf("reset=%d start=%d wait=%d speak=%d", stub.resets, stub.starts, stub.waits, stub.speaks)
	}
	var rows []runResult
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("json %v out=%s", err, out.Bytes())
	}
	if len(rows) != 1 {
		t.Fatalf("want array of 1, got %v", rows)
	}
	r := rows[0]
	if r.DeviceID != "sim_1" || r.TurnID != "turn_1" || r.Verdict != "replied" ||
		r.DownBytes != 320 || r.DownFormat != "pcm" || r.Overridden {
		t.Fatalf("%+v", r)
	}

	stub.state, stub.overridden = "running", true
	out = withIO(t)
	code = simctl([]string{"--listen", host, "run", "--asset", "ast_x"})
	if code == 0 || !bytes.Contains(out.Bytes(), []byte("我不动它")) {
		t.Fatalf("dirty 应报错, code=%d out=%s", code, out.Bytes())
	}

	out = withIO(t)
	code = simctl([]string{"--listen", host, "run", "--asset", "ast_x", "--dirty"})
	if code != 0 {
		t.Fatalf("--dirty 应放行 code=%d out=%s", code, out.Bytes())
	}
}

func TestTurnAudioHistory(t *testing.T) {
	mux := http.NewServeMux()
	// source 只在列表端点上；单条 /turns/{id} 不返，所以 turn 走列表再挑。
	mux.HandleFunc("GET /devices/{id}/turns", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"source":"live","turns":[{"turn_id":"other"},{"turn_id":"turn_1","down_bytes":1}]}`))
	})
	mux.HandleFunc("GET /devices/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"events":[{"turn_id":"turn_1","event_type":"turn_terminal"},{"turn_id":"other","event_type":"x"}]}`))
	})
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}/frames", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"direction":"outbound","payload_len":4}` + "\n"))
	})
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}/audio/downlink", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("WAV"))
	})
	mux.HandleFunc("GET /devices/{id}/instances", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"device_id":"sim_1","instances":[{"instance_id":"ins_old","source":"disk"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	out := withIO(t)
	if code := simctl([]string{"--listen", host, "turn", "sim_1", "--turn", "turn_1", "--instance", "ins_1"}); code != 0 {
		t.Fatalf("turn code=%d out=%s", code, out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"event_type":"turn_terminal"`)) || bytes.Contains(out.Bytes(), []byte(`"event_type":"x"`)) {
		t.Fatalf("应按 turn_id 过滤事件: %s", out.Bytes())
	}
	// source 恒空是真出过的 bug：曾从 /events 读，而那个端点不返这个字段。
	if !bytes.Contains(out.Bytes(), []byte(`"source":"live"`)) {
		t.Fatalf("source 应取自 /turns 列表: %s", out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"down_bytes":1`)) {
		t.Fatalf("应从列表里挑出 turn_1 这一轮: %s", out.Bytes())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "down.wav")
	out = withIO(t)
	if code := simctl([]string{"--listen", host, "audio", "sim_1", "--turn", "turn_1", "--instance", "ins_1", "--side", "downlink", "--out", path}); code != 0 {
		t.Fatalf("audio code=%d out=%s", code, out.Bytes())
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "WAV" {
		t.Fatalf("file=%q err=%v", b, err)
	}

	out = withIO(t)
	if code := simctl([]string{"--listen", host, "history", "sim_1"}); code != 0 {
		t.Fatalf("history code=%d out=%s", code, out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte("ins_old")) {
		t.Fatalf("%s", out.Bytes())
	}
}
