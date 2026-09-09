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
		"--asset", "--parallel", "--dirty", "--force", "--env", "--enterprise", "--device-type",
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
	leaseTries, releases          int
	state                         string
	overridden                    bool
	waitFails                     bool            // wait_ready 回 409 generation_gone
	speakFails                    bool            // speak_and_wait 回 500
	lastError                     string          // GET /devices/{id} 的 last_error
	devices                       []deviceRow     // 空 = 只有 sim_1 那台（老用法）
	busy                          map[string]bool // 这些 device_id 的租约回 409
}

// rows 空 devices 时合成老的单台 sim_1，保住既有用例不用改。
func (s *runStub) rows() []deviceRow {
	if len(s.devices) > 0 {
		return s.devices
	}
	return []deviceRow{{
		DeviceID: "sim_1", InstanceID: "ins_1", InstanceState: s.state,
		ConnGeneration: 1, Environment: "local", Enterprise: "vp",
		DeviceType: "spk", Overridden: s.overridden,
	}}
}

func (s *runStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": s.rows()})
	})
	mux.HandleFunc("POST /devices/{id}/lease", func(w http.ResponseWriter, r *http.Request) {
		s.leaseTries++
		id := r.PathValue("id")
		if s.busy[id] {
			http.Error(w, `{"error":"lease_held","owner":"other"}`, 409)
			return
		}
		var row deviceRow
		for _, d := range s.rows() {
			if d.DeviceID == id {
				row = d
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_id": id, "lease_id": "lse_" + id, "device": row,
		})
	})
	mux.HandleFunc("DELETE /devices/{id}/lease", func(w http.ResponseWriter, _ *http.Request) {
		s.releases++
		_, _ = w.Write([]byte(`{"released":true}`))
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
		if s.waitFails {
			http.Error(w, `{"error":"generation_gone"}`, 409)
			return
		}
		_, _ = w.Write([]byte(`{"device_id":"sim_1","instance_id":"ins_1"}`))
	})
	mux.HandleFunc("GET /devices/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": "sim_1", "last_error": s.lastError})
	})
	mux.HandleFunc("POST /devices/{id}/speak_and_wait", func(w http.ResponseWriter, _ *http.Request) {
		s.speaks++
		if s.speakFails {
			http.Error(w, `{"error":"speak 炸了"}`, 500)
			return
		}
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

// 真机实测出来的：设备连上了但注册没被 ack，run 只吐 generation_gone，
// 而真正的原因（register ACK 超时）在设备的 last_error 里。不带出来 agent 就断线了。
func TestRunNotReadyCarriesLastError(t *testing.T) {
	stub := &runStub{state: "stopped", waitFails: true, lastError: "register ACK 超时"}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	out := withIO(t)
	code := simctl([]string{"--listen", strings.TrimPrefix(srv.URL, "http://"), "run", "--asset", "ast_x"})
	if code == 0 {
		t.Fatalf("起不来应非 0 退出: %s", out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte("register ACK 超时")) {
		t.Fatalf("错误里应带 last_error: %s", out.Bytes())
	}
}

// 门禁拒绝不是连接问题，不该去捞 last_error 混淆视听。
func TestDirtyErrorNotAnnotated(t *testing.T) {
	stub := &runStub{state: "running", overridden: true, lastError: "不该出现"}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	out := withIO(t)
	if code := simctl([]string{"--listen", strings.TrimPrefix(srv.URL, "http://"), "run", "--asset", "ast_x"}); code == 0 {
		t.Fatalf("应拒绝: %s", out.Bytes())
	}
	if bytes.Contains(out.Bytes(), []byte("不该出现")) {
		t.Fatalf("门禁错误不该带 last_error: %s", out.Bytes())
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

// ——— 选择语义：过滤粒度决定跑几台 ———

func poolStub(t *testing.T) (*runStub, string) {
	t.Helper()
	stub := &runStub{devices: []deviceRow{
		{DeviceID: "a1", InstanceID: "ins_a1", InstanceState: "running", ConnGeneration: 1,
			Environment: "local", Enterprise: "vp", DeviceType: "A3"},
		{DeviceID: "a2", InstanceID: "ins_a2", InstanceState: "running", ConnGeneration: 1,
			Environment: "local", Enterprise: "vp", DeviceType: "A3"},
		{DeviceID: "a3", InstanceID: "ins_a3", InstanceState: "running", ConnGeneration: 1,
			Environment: "local", Enterprise: "vp", DeviceType: "A3"},
		{DeviceID: "b1", InstanceID: "ins_b1", InstanceState: "running", ConnGeneration: 1,
			Environment: "local", Enterprise: "vp", DeviceType: "MIC"},
	}, busy: map[string]bool{}}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	return stub, strings.TrimPrefix(srv.URL, "http://")
}

func decodeRows(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("json %v out=%s", err, out.Bytes())
	}
	return rows
}

func TestRunRandomOneOfType(t *testing.T) {
	stub, host := poolStub(t)
	out := withIO(t)
	if code := simctl([]string{"--listen", host, "run", "--device-type", "A3", "--asset", "x"}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	rows := decodeRows(t, out)
	if len(rows) != 1 {
		t.Fatalf("--device-type 应只跑一台，得到 %d 台: %s", len(rows), out.Bytes())
	}
	got, _ := rows[0]["device_id"].(string)
	if got != "a1" && got != "a2" && got != "a3" {
		t.Fatalf("跑的应是 A3 类型里的一台，得到 %q", got)
	}
	if stub.speaks != 1 || stub.releases != 1 {
		t.Fatalf("speaks=%d releases=%d，应各 1 次", stub.speaks, stub.releases)
	}
}

func TestRunRandomSpreadsOverCandidates(t *testing.T) {
	in := []deviceRow{{DeviceID: "a1"}, {DeviceID: "a2"}, {DeviceID: "a3"}}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		seen[shuffled(in)[0].DeviceID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("50 次洗牌首选应不止一台，得到 %v", seen)
	}
	// 洗牌不得改动入参。
	if in[0].DeviceID != "a1" || in[2].DeviceID != "a3" {
		t.Fatalf("shuffled 污染了入参: %v", in)
	}
}

// 三台里只留 a3 空闲，跑 5 次都必须落到 a3。洗牌把 a3 排第一的概率是 1/3，
// 所以不跳台的实现有 1-(1/3)^5 ≈ 99.6% 会在这里挂；正确实现则永远绿。
// 不断言 leaseTries 的具体值——它取决于洗牌顺序，钉死了就是 flaky。
func TestRunRandomHopsOnBusy(t *testing.T) {
	stub, host := poolStub(t)
	stub.busy["a1"], stub.busy["a2"] = true, true
	for i := 0; i < 5; i++ {
		out := withIO(t)
		if code := simctl([]string{"--listen", host, "run", "--device-type", "A3", "--asset", "x"}); code != 0 {
			t.Fatalf("第 %d 次应换到空闲那台，code=%d out=%s", i, code, out.Bytes())
		}
		rows := decodeRows(t, out)
		if len(rows) != 1 || rows[0]["device_id"] != "a3" {
			t.Fatalf("第 %d 次应落到 a3: %s", i, out.Bytes())
		}
	}
	if stub.speaks != 5 || stub.releases != 5 {
		t.Fatalf("speaks=%d releases=%d，应各 5 次", stub.speaks, stub.releases)
	}
}

func TestRunRandomAllBusy(t *testing.T) {
	stub, host := poolStub(t)
	stub.busy["a1"], stub.busy["a2"], stub.busy["a3"] = true, true, true
	out := withIO(t)
	if code := simctl([]string{"--listen", host, "run", "--device-type", "A3", "--asset", "x"}); code == 0 {
		t.Fatalf("全被占应非 0 退出: %s", out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte("lease_held")) || stub.speaks != 0 {
		t.Fatalf("应报全被占且一次都没送话: speaks=%d out=%s", stub.speaks, out.Bytes())
	}
}

// 只对争用跳台，绝不对失败跳台——否则一台真起不来会被安静换掉，故障就藏起来了。
func TestRunRandomDoesNotHopOnRealFailure(t *testing.T) {
	stub, host := poolStub(t)
	stub.speakFails = true
	out := withIO(t)
	if code := simctl([]string{"--listen", host, "run", "--device-type", "A3", "--asset", "x"}); code == 0 {
		t.Fatalf("送话失败应非 0 退出: %s", out.Bytes())
	}
	if stub.speaks != 1 {
		t.Fatalf("失败不该换台重试，speaks=%d", stub.speaks)
	}
	rows := decodeRows(t, out)
	if len(rows) != 1 || rows[0]["error"] == nil {
		t.Fatalf("应是带 error 的单元素数组: %s", out.Bytes())
	}
}

// 回归钉：批量语义没被随机改掉。
func TestRunFilterOnlyEnterpriseStillRunsAll(t *testing.T) {
	stub, host := poolStub(t)
	out := withIO(t)
	if code := simctl([]string{"--listen", host, "run", "--enterprise", "vp", "--asset", "x"}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if rows := decodeRows(t, out); len(rows) != 4 {
		t.Fatalf("只给 --enterprise 应全跑 4 台，得到 %d: %s", len(rows), out.Bytes())
	}
	if stub.speaks != 4 || stub.releases != 4 {
		t.Fatalf("speaks=%d releases=%d，应各 4 次", stub.speaks, stub.releases)
	}
}

func TestRunBatchBusyIsErrorElement(t *testing.T) {
	stub, host := poolStub(t)
	stub.busy["a1"] = true
	out := withIO(t)
	code := simctl([]string{"--listen", host, "run", "--env", "local", "--asset", "x"})
	if code == 0 {
		t.Fatalf("有一台被占应非 0 退出: %s", out.Bytes())
	}
	rows := decodeRows(t, out)
	if len(rows) != 4 {
		t.Fatalf("数组要和选中集一一对应（4 台），得到 %d: %s", len(rows), out.Bytes())
	}
	var errs int
	for _, r := range rows {
		if r["error"] != nil {
			errs++
		}
	}
	if errs != 1 || stub.speaks != 3 {
		t.Fatalf("应正好 1 个 error 元素、3 台真跑：errs=%d speaks=%d", errs, stub.speaks)
	}
}

func TestRunReleasesLeaseOnFailure(t *testing.T) {
	stub, host := poolStub(t)
	stub.speakFails = true
	out := withIO(t)
	simctl([]string{"--listen", host, "run", "a1", "--asset", "x"})
	if stub.releases != 1 {
		t.Fatalf("失败路径也要还租约，releases=%d out=%s", stub.releases, out.Bytes())
	}
}

// 点名了就是那台，被占直接报错，不换台。
func TestRunSingleDeviceLeaseHeld(t *testing.T) {
	stub, host := poolStub(t)
	stub.busy["a1"] = true
	out := withIO(t)
	if code := simctl([]string{"--listen", host, "run", "a1", "--asset", "x"}); code == 0 {
		t.Fatalf("点名那台被占应非 0 退出: %s", out.Bytes())
	}
	if stub.leaseTries != 1 || stub.speaks != 0 {
		t.Fatalf("不该换台：leaseTries=%d speaks=%d", stub.leaseTries, stub.speaks)
	}
}

// --force 漏进 boolFlag 白名单的话，sim_1 会被当成它的值吞掉，静默跑错设备。
func TestFlagsFirstForceIsBool(t *testing.T) {
	got := flagsFirst([]string{"sim_1", "--force", "--asset", "ast_x"})
	if got[len(got)-1] != "sim_1" {
		t.Fatalf("--force 后的位置参数被吞了: %v", got)
	}
}
