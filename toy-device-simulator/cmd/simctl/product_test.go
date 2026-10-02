package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Phase 12：run 选产品、带临时覆盖，结果里带出设备实际的产品、覆盖与拍照摘要。

// productStub 是 Phase 12 的 manager 桩：记下 start 请求体，回设备的产品、覆盖与本轮事件。
type productStub struct {
	mu         sync.Mutex
	state      string
	overridden bool
	product    string
	overrides  map[string]any
	events     []map[string]any
	startBody  map[string]any
	starts     int
	resets     int
	speaks     int
	speakBody  map[string]any // 最后一次 speak_and_wait 的请求体
}

func (s *productStub) rowLocked() map[string]any {
	ov := s.overrides
	if ov == nil {
		ov = map[string]any{}
	}
	return map[string]any{
		"device_id": "sim_1", "instance_id": "ins_1", "instance_state": s.state,
		"conn_generation": 1, "environment": "local", "enterprise": "vp", "device_type": "A3",
		"overridden": s.overridden, "product": s.product, "overrides": ov,
	}
}

func (s *productStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []any{s.rowLocked()}})
	})
	mux.HandleFunc("GET /devices/{id}", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.rowLocked())
	})
	mux.HandleFunc("POST /devices/{id}/lease", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": "sim_1", "lease_id": "lse_1", "device": s.rowLocked()})
	})
	mux.HandleFunc("DELETE /devices/{id}/lease", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"released":true}`))
	})
	mux.HandleFunc("POST /devices/{id}/config/reset", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.resets++
		s.overridden, s.overrides = false, nil
		_, _ = w.Write([]byte(`{"overridden":false}`))
	})
	mux.HandleFunc("POST /devices/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.starts++
		s.startBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&s.startBody)
		s.state = "running"
		s.product = "test"
		if p, _ := s.startBody["product"].(string); p != "" {
			s.product = p
		}
		if ov, ok := s.startBody["overrides"].(map[string]any); ok {
			s.overrides, s.overridden = ov, len(ov) > 0
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_id": "sim_1", "instance_id": "ins_1", "conn_generation": 2, "product": s.product,
		})
	})
	mux.HandleFunc("POST /devices/{id}/wait_ready", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"device_id":"sim_1","instance_id":"ins_1"}`))
	})
	mux.HandleFunc("POST /devices/{id}/speak_and_wait", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.speaks++
		s.speakBody = body
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"turn_id":"turn_1","instance_id":"ins_1","turn_end_reason":"idle","uplink_end_reason":"complete","reply_kind":"tts"}`))
	})
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"turn_id":"turn_1","instance_id":"ins_1","turn_end_reason":"idle","uplink_end_reason":"complete","reply_kind":"tts","up_format":"pcm","down_format":"pcm","down_bytes":320}`))
	})
	mux.HandleFunc("GET /devices/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		evs := s.events
		if evs == nil {
			evs = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": evs})
	})
	mux.HandleFunc("GET /products", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"products":[{"id":"default","name":"默认产品"},{"id":"mh8w","name":"MH8W"}]}`))
	})
	return mux
}

func (s *productStub) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// runArgs 拼一次 run：挂靠三级与资产固定，extra 叠在后面。
func runArgs(host string, extra ...string) []string {
	args := []string{"--listen", host, "run", "--env", "local", "--enterprise", "vp", "--device-type", "A3", "--asset", "ast_x"}
	return append(args, extra...)
}

func TestHelpListsProductFlags(t *testing.T) {
	var errb bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = io.Discard, &errb
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
	if code := simctl([]string{"--help"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, v := range []string{"--product", "--set", "products"} {
		if !strings.Contains(errb.String(), v) {
			t.Fatalf("help 缺 %q", v)
		}
	}
}

func TestParseSets(t *testing.T) {
	got, err := parseSets([]string{
		"audio.format=mp3",
		"behavior.first_reply_timeout_sec=7",
		"features.photo.enabled=true",
		`nic_iccid="8986"`,
		"recording.output_dir=a=b",
	})
	if err != nil {
		t.Fatalf("parseSets: %v", err)
	}
	want := map[string]any{
		"audio.format":                     "mp3",
		"behavior.first_reply_timeout_sec": float64(7),
		"features.photo.enabled":           true,
		"nic_iccid":                        "8986", // 带引号才是字符串；不带会按数字发出去
		"recording.output_dir":             "a=b",  // 只按第一个 = 切
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSets 得到 %#v", got)
	}
	for _, bad := range []string{"noequals", "=mp3"} {
		if _, err := parseSets([]string{bad}); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

func TestRunSendsProductAndOverridesOnStart(t *testing.T) {
	stub := &productStub{state: "stopped"}
	host := stub.serve(t)
	out := withIO(t)
	code := simctl(runArgs(host, "--product", "mh8w", "--set", "audio.format=mp3", "--set", "behavior.first_reply_timeout_sec=7"))
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if stub.resets != 0 || stub.starts != 1 {
		t.Fatalf("没有覆盖的停机设备不该 reset：resets=%d starts=%d", stub.resets, stub.starts)
	}
	b := stub.startBody
	if b["environment"] != "local" || b["enterprise"] != "vp" || b["device_type"] != "A3" || b["product"] != "mh8w" {
		t.Fatalf("start 请求应带挂靠三级与 product: %v", b)
	}
	ov, _ := b["overrides"].(map[string]any)
	if len(ov) != 2 || ov["audio.format"] != "mp3" || ov["behavior.first_reply_timeout_sec"] != float64(7) {
		t.Fatalf("start 请求的 overrides 应是两条 --set: %v", b)
	}
}

// 不给 --product 就不发 product 字段，让服务端用设备类型的默认产品；不给 --set 就不发 overrides。
func TestRunWithoutProductOrSetOmitsFields(t *testing.T) {
	stub := &productStub{state: "stopped"}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host)); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if _, ok := stub.startBody["product"]; ok {
		t.Fatalf("不给 --product 时不得发 product: %v", stub.startBody)
	}
	if _, ok := stub.startBody["overrides"]; ok {
		t.Fatalf("不给 --set 时不得发 overrides: %v", stub.startBody)
	}
}

func TestRunStoppedWithOverridesResetsThenStarts(t *testing.T) {
	stub := &productStub{state: "stopped", overridden: true, product: "test",
		overrides: map[string]any{"audio.format": "wav"}}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host, "--set", "audio.format=mp3")); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if stub.resets != 1 || stub.starts != 1 {
		t.Fatalf("停机且有覆盖应先 reset 再 start：resets=%d starts=%d", stub.resets, stub.starts)
	}
	if ov, _ := stub.startBody["overrides"].(map[string]any); ov["audio.format"] != "mp3" {
		t.Fatalf("start 应带这次的 --set: %v", stub.startBody)
	}
	// overridden 报设备当前值：reset 后又带 --set 起来，现在就是有覆盖。
	if !bytes.Contains(out.Bytes(), []byte(`"overridden":true`)) {
		t.Fatalf("结果的 overridden 应和 overrides 对得上: %s", out.Bytes())
	}
}

// 在跑、覆盖与这次 --set 相同：就是上一次 run 留下的，复用，不算脏。
func TestRunRunningSameOverridesReuses(t *testing.T) {
	stub := &productStub{state: "running", overridden: true, product: "test",
		overrides: map[string]any{"audio.format": "mp3"}}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host, "--set", "audio.format=mp3")); code != 0 {
		t.Fatalf("覆盖相同应复用，code=%d out=%s", code, out.Bytes())
	}
	if stub.starts != 0 || stub.speaks != 1 {
		t.Fatalf("应复用在跑的设备：starts=%d speaks=%d", stub.starts, stub.speaks)
	}
}

func TestRunRunningDifferentOverridesIsDirty(t *testing.T) {
	stub := &productStub{state: "running", overridden: true, product: "test",
		overrides: map[string]any{"audio.format": "mp3"}}
	host := stub.serve(t)
	out := withIO(t)
	code := simctl(runArgs(host, "--set", "audio.format=wav"))
	if code == 0 || !bytes.Contains(out.Bytes(), []byte("我不动它")) || stub.speaks != 0 {
		t.Fatalf("覆盖不同应报脏且不送话：code=%d speaks=%d out=%s", code, stub.speaks, out.Bytes())
	}
	out = withIO(t)
	if code := simctl(runArgs(host, "--set", "audio.format=wav", "--dirty")); code != 0 {
		t.Fatalf("--dirty 应放行，code=%d out=%s", code, out.Bytes())
	}
}

// 在跑且没有覆盖：这次的 --product / --set 静默不生效，结果里的 product / overrides 是设备实际值。
func TestRunRunningWithoutOverridesIgnoresRequestSilently(t *testing.T) {
	stub := &productStub{state: "running", product: "test"}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host, "--product", "mh8w", "--set", "audio.format=wav")); code != 0 {
		t.Fatalf("静默复用应成功，code=%d out=%s", code, out.Bytes())
	}
	if stub.starts != 0 {
		t.Fatalf("在跑的设备不该重新 start，starts=%d", stub.starts)
	}
	r := decodeRows(t, out)[0]
	if r["product"] != "test" {
		t.Fatalf("结果的 product 应是设备实际的 test: %s", out.Bytes())
	}
	if ov, _ := r["overrides"].(map[string]any); len(ov) != 0 {
		t.Fatalf("结果的 overrides 应是设备实际的空值: %s", out.Bytes())
	}
}

func TestRunResultCarriesProductOverridesAndPhoto(t *testing.T) {
	stub := &productStub{state: "stopped", events: []map[string]any{
		{"event_type": "photo_command", "turn_id": "turn_1", "reason": "0123456789abcdef"},
		{"event_type": "photo_uploaded", "turn_id": "turn_1", "reason": "bytes=1024 slices=1"},
		{"event_type": "photo_skipped", "turn_id": "other_turn", "reason": "别的轮，不该算进来"},
	}}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host, "--product", "mh8w", "--set", "audio.format=mp3")); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	r := decodeRows(t, out)[0]
	if r["product"] != "mh8w" {
		t.Fatalf("结果应带 product=mh8w: %s", out.Bytes())
	}
	if ov, _ := r["overrides"].(map[string]any); ov["audio.format"] != "mp3" {
		t.Fatalf("结果应带设备实际的覆盖: %s", out.Bytes())
	}
	photo, _ := r["photo"].(map[string]any)
	if photo["command"] != true || photo["uploaded"] != true || photo["skipped"] != "" {
		t.Fatalf("photo 摘要应取自本轮事件: %s", out.Bytes())
	}
}

// --image 随每次送话带上 image_asset_id，结果回显（phase14 §6）。
func TestRunImagePassesImageAssetID(t *testing.T) {
	stub := &productStub{state: "stopped", events: []map[string]any{
		{"event_type": "photo_uploaded", "turn_id": "turn_1", "reason": "source=speak asset=img_1 uuid=7 bytes=10 slices=1"},
	}}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host, "--image", "img_1")); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	r := decodeRows(t, out)[0]
	photo, _ := r["photo"].(map[string]any)
	if r["image_asset_id"] != "img_1" || photo["command"] != false || photo["uploaded"] != true {
		t.Fatalf("结果应带 image_asset_id，photo 为 command=false、uploaded=true: %s", out.Bytes())
	}
	if stub.speakBody["image_asset_id"] != "img_1" {
		t.Fatalf("speak_and_wait 应带 image_asset_id: %v", stub.speakBody)
	}
}

func TestRunWithoutImageOmitsImageAssetID(t *testing.T) {
	stub := &productStub{state: "stopped"}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host)); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if _, has := stub.speakBody["image_asset_id"]; has {
		t.Fatalf("不带 --image 时请求体不该有 image_asset_id: %v", stub.speakBody)
	}
	if r := decodeRows(t, out)[0]; r["image_asset_id"] != "" {
		t.Fatalf("没带图时结果的 image_asset_id 应为空串: %s", out.Bytes())
	}
}

func TestRunPhotoSkippedReason(t *testing.T) {
	stub := &productStub{state: "stopped", events: []map[string]any{
		{"event_type": "photo_command", "turn_id": "turn_1", "reason": "0123456789abcdef"},
		{"event_type": "photo_skipped", "turn_id": "turn_1", "reason": "产品没开拍照"},
	}}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl(runArgs(host)); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	photo, _ := decodeRows(t, out)[0]["photo"].(map[string]any)
	if photo["command"] != true || photo["uploaded"] != false || photo["skipped"] != "产品没开拍照" {
		t.Fatalf("photo.skipped 应带原因: %s", out.Bytes())
	}
}

func TestProductsVerb(t *testing.T) {
	stub := &productStub{state: "stopped"}
	host := stub.serve(t)
	out := withIO(t)
	if code := simctl([]string{"--listen", host, "products"}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.Bytes())
	}
	if !bytes.Contains(out.Bytes(), []byte("mh8w")) {
		t.Fatalf("products 应原样输出 GET /products: %s", out.Bytes())
	}
}
