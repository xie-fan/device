package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"toy-device-simulator/core"
	"toy-device-simulator/manager"
	"toy-device-simulator/media"
	"toy-device-simulator/protocol"
)

type autoOpts struct {
	replyTTS bool
	failJSON bool
	needAck  bool
	hold     bool
	silent   bool
	// echoLimit>0 时只回显前 N 个 report echo（用于探针 echo 超时用例）。
	echoLimit int
	// Phase 5e：定制 TTS 回复的帧头格式 / 采样率 / 内容（缺省 pcm/16000/{1,2,3,4}）。
	ttsFormat     string
	ttsSampleRate int
	ttsPayload    []byte
	// Phase 12 拍照：收到第一片上行音频后下发拍照指令（movement.behavior=601）。
	photoCmd        bool
	photoKey        string // 缺省 0123456789abcdef
	photoStartVoice []byte // 非空时 base64 进 start_voice
	// photoReply：收到 Stage=2 的图片末片后，隔 photoReplyDelay 下发一帧 UUID=0 的语音回复。
	photoReply        bool
	photoReplyDelay   time.Duration
	photoReplyPayload []byte // 缺省 {9,9,9,9}
}

type testEnv struct {
	t         *testing.T
	srv       *httptest.Server
	client    *http.Client
	cfg       manager.Config
	recDir    string
	registry  string
	mu        sync.Mutex
	conns     map[string]*fakeConn
	dialURLs  map[string]string
	auto      autoOpts
	afterStat func()
	// overrides 是 createBody 记下的「这台设备的属性」（点路径），startDevice 时作为
	// 临时覆盖带上。Phase 12 起设备只有 id，属性不再随建设备走。
	overrides map[string]map[string]any
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnvFull(t, nil, 0)
}

func newEnvCfg(t *testing.T, mut func(*manager.Config)) *testEnv {
	t.Helper()
	return newEnvFull(t, mut, 0)
}

func newEnvTTL(t *testing.T, d time.Duration) *testEnv {
	t.Helper()
	return newEnvFull(t, nil, d)
}

func newEnvFull(t *testing.T, mut func(*manager.Config), ttl time.Duration) *testEnv {
	t.Helper()
	e := &testEnv{
		t:         t,
		recDir:    t.TempDir(),
		registry:  filepath.Join(t.TempDir(), "registry.yaml"),
		conns:     map[string]*fakeConn{},
		overrides: map[string]map[string]any{},
		client:    &http.Client{Timeout: 8 * time.Second},
		cfg: manager.Config{
			MaxConnections:        32,
			MaxConcurrentSpeaking: 8,
			DefaultStaggerMs:      50,
			PerDeviceBufferBytes:  1048576,
			WriteQueueDepth:       256,
			WriteDrainTimeoutSec:  2,
			EventLogMaxEntries:    10000,
			EventLogTTLHours:      24,
			AssetsRoot:            t.TempDir(),
			MaxAssetBytes:         10 * 1024 * 1024,
			MaxAssetDurationSec:   60,
			MaxStreamEntries:      16,
			MaxStreamDurationSec:  60,
			WaitReadyTimeoutSec:   30,
		},
	}
	if mut != nil {
		mut(&e.cfg)
	}
	if e.cfg.AssetsRoot == "" {
		e.cfg.AssetsRoot = t.TempDir()
	}
	e.start(t, ttl)
	e.seedRegistry(t)
	return e
}

// sharedToolchain 测试共享的 ffmpeg 工具链：探测一次；无 ffmpeg 环境返回 nil，
// 依赖转码的用例自行 skip。
var (
	tcOnce   sync.Once
	sharedTC *media.Toolchain
)

func sharedToolchain() *media.Toolchain {
	tcOnce.Do(func() { sharedTC, _ = media.Detect("") })
	return sharedTC
}

// start 用当前 Options 建 Server；restart 复用同一 registry 路径验证落盘。
func (e *testEnv) start(t *testing.T, ttl time.Duration) {
	t.Helper()
	h, err := New(Options{
		Config:        e.cfg,
		Dial:          e.dial,
		RecordingsDir: e.recDir,
		RegistryPath:  e.registry,
		TTL:           ttl,
		Media:         sharedToolchain(),
		AfterAssetStat: func() {
			if e.afterStat != nil {
				e.afterStat()
			}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.srv = httptest.NewServer(h)
	t.Cleanup(func() {
		if c, ok := h.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		e.srv.Close()
	})
}

// seedRegistry 预置 本地 环境 + demo 厂商 + A3 类型 + 测试产品 test（A3 的默认产品）。
func (e *testEnv) seedRegistry(t *testing.T) {
	t.Helper()
	if code, body := e.post(t, "/registry/environments", map[string]any{
		"name": "local", "url": "ws://127.0.0.1:1/",
	}); code != http.StatusCreated {
		t.Fatalf("seed env 应 201，得到 %d body=%s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises", map[string]any{
		"name": "演示厂商", "short_name": "demo",
	}); code != http.StatusCreated {
		t.Fatalf("seed enterprise 应 201，得到 %d body=%s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises/demo/device_types", map[string]any{
		"name": "A3 音箱", "short_name": "A3",
	}); code != http.StatusCreated {
		t.Fatalf("seed device_type 应 201，得到 %d body=%s", code, body)
	}
	if code, body := e.post(t, "/products", e.testProductBody()); code != http.StatusCreated {
		t.Fatalf("seed product 应 201，得到 %d body=%s", code, body)
	}
	if code, body := e.put(t, "/registry/environments/local/enterprises/demo/device_types/A3", map[string]any{
		"name": "A3 音箱", "default_product": "test",
	}); code != http.StatusOK {
		t.Fatalf("seed default_product 应 200，得到 %d body=%s", code, body)
	}
}

func (e *testEnv) dial(url string, h http.Header) (core.Conn, error) {
	c := newFakeConn()
	if e.auto.hold {
		c.writeGate = make(chan struct{})
	}
	startAuto(c, e.auto)
	id := deviceIDFromHeader(h.Get("Device"))
	e.mu.Lock()
	e.conns[id] = c
	if e.dialURLs == nil {
		e.dialURLs = map[string]string{}
	}
	e.dialURLs[id] = url
	e.mu.Unlock()
	return c, nil
}

func (e *testEnv) dialURL(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dialURLs[id]
}

func (e *testEnv) conn(id string) *fakeConn {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conns[id]
}

func deviceIDFromHeader(dev string) string {
	if i := lastSlash(dev); i >= 0 && i+1 < len(dev) {
		return dev[i+1:]
	}
	return dev
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func (e *testEnv) url(path string) string {
	return e.srv.URL + path
}

func (e *testEnv) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.url(path), rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func (e *testEnv) post(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	return e.do(t, http.MethodPost, path, body)
}

func (e *testEnv) put(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	return e.do(t, http.MethodPut, path, body)
}

func (e *testEnv) get(t *testing.T, path string) (int, []byte, http.Header) {
	t.Helper()
	resp, err := e.client.Get(e.url(path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b, resp.Header.Clone()
}

func (e *testEnv) del(t *testing.T, path string) (int, []byte) {
	t.Helper()
	return e.do(t, http.MethodDelete, path, nil)
}

func decodeMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if len(body) == 0 {
		return m
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("JSON 解析失败: %v body=%s", err, body)
	}
	return m
}

func strField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func boolField(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func intField(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func wavPCM(sampleRate int) []byte {
	return wavPCMDur(sampleRate, 100)
}

func wavPCMDur(sampleRate, ms int) []byte {
	n := sampleRate * ms / 1000 * 2 // s16le mono
	return core.EncodeWAV(core.PCM{
		Samples:       bytes.Repeat([]byte{1, 0}, n/2),
		SampleRate:    sampleRate,
		Channels:      1,
		BitsPerSample: 16,
	})
}

func (e *testEnv) postAsset(t *testing.T, filename string, data []byte) (int, []byte) {
	t.Helper()
	return e.postAssetFields(t, filename, data, nil)
}

// postAssetFields 额外携带表单字段（raw PCM 的 fmt 三项等）。
func (e *testEnv) postAssetFields(t *testing.T, filename string, data []byte, fields map[string]string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.url("/assets"), &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// deviceBody 是 Phase 12 之前一台设备的全部属性。现在它有两个用处：
// 测试产品 test 的默认值就是这一套（testProductBody），老用例改几个字段后交给
// createBody，改动的字段会作为临时覆盖在 startDevice 时带上。
func (e *testEnv) deviceBody(id string) map[string]any {
	return map[string]any{
		"device_id":        id,
		"action":           "chatbot",
		"playing_mode":     1,
		"firmware_version": "1.0.0",
		"nic_type":         "wifi",
		"nic_iccid":        "8986",
		"audio": map[string]any{
			"format":           "pcm",
			"sample_rate":      16000,
			"channels":         1,
			"sample_format":    "s16le",
			"slice_ms":         100,
			"max_payload_size": 51200,
		},
		"behavior": map[string]any{
			"auto_register":          true,
			"auto_report":            true,
			"keepalive_interval_sec": 60,
			"report_sequence_start":  1,
			"downlink_ack":           map[string]any{"mode": "binary", "sleep_ms": 0, "code": 0},
		},
		"uuid":      map[string]any{"min": 1, "max": 2147483647},
		"recording": map[string]any{"enable_frame_log": true, "save_uplink_audio": true, "save_downlink_audio": true, "output_dir": e.recDir},
	}
}

// testProductBody 是 seed 的测试产品：默认值即 deviceBody 那一套，所以老用例拿到的
// 还是原来的设备。清单放宽，免得覆盖格式的用例和推荐项纠缠。
func (e *testEnv) testProductBody() map[string]any {
	defaults := e.deviceBody("")
	delete(defaults, "device_id")
	return map[string]any{
		"id":            "test",
		"name":          "测试产品",
		"playing_modes": []int{1, 2, 3},
		"audio_formats": []string{"pcm/16000", "wav/16000", "mp3/16000", "amr/16000", "amr/8000", "aac/16000"},
		"defaults":      defaults,
	}
}

// createBody 给出 Phase 12 的建设备体（只有 device_id），并记下 dev 里的属性，
// 由 startDevice 作为临时覆盖带上——与产品默认值相同的字段服务端不计入覆盖。
func (e *testEnv) createBody(dev map[string]any) map[string]any {
	id, _ := dev["device_id"].(string)
	e.mu.Lock()
	e.overrides[id] = flattenOverrides("", dev)
	e.mu.Unlock()
	return map[string]any{"device_id": id}
}

// flattenOverrides 把嵌套属性展开成点路径（顶层的 device_id 跳过）。
func flattenOverrides(prefix string, m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if prefix == "" && k == "device_id" {
			continue
		}
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			for sk, sv := range flattenOverrides(key, sub) {
				out[sk] = sv
			}
			continue
		}
		out[key] = v
	}
	return out
}

// overridesFor 取 createBody 记下的覆盖，给自己拼 start / batch_start 请求的用例用。
func (e *testEnv) overridesFor(id string) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.overrides[id]
}

// seedBinding 是 seedRegistry 建出来的那棵树上的三级，测试默认都挂这里。
func (e *testEnv) seedBinding() map[string]any {
	return map[string]any{"environment": "local", "enterprise": "demo", "device_type": "A3"}
}

func (e *testEnv) createDevice(t *testing.T, id string) (instanceID string) {
	t.Helper()
	code, body := e.post(t, "/devices", map[string]any{"device_id": id})
	if code != http.StatusCreated {
		t.Fatalf("POST /devices 应 201，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	inst, _ := m["instances"].([]any)
	if len(inst) == 0 {
		t.Fatalf("201 应含 instances，body=%s", body)
	}
	row, _ := inst[0].(map[string]any)
	instanceID = strField(row, "instance_id")
	if instanceID == "" {
		t.Fatalf("instance_id 为空，body=%s", body)
	}
	return instanceID
}

// startDevice 挂 seed 的三级、显式用测试产品，并带上 createBody 记下的覆盖。
// 显式给 product：有的用例会删掉再重建 A3，重建出来的类型没有默认产品。
func (e *testEnv) startDevice(t *testing.T, id string) (instanceID string, gen int) {
	t.Helper()
	req := e.seedBinding()
	req["product"] = "test"
	if ov := e.overridesFor(id); len(ov) > 0 {
		req["overrides"] = ov
	}
	start := time.Now()
	code, body := e.post(t, "/devices/"+id+"/start", req)
	if code != http.StatusAccepted {
		t.Fatalf("POST start 应 202，得到 %d body=%s", code, body)
	}
	if time.Since(start) > time.Second {
		t.Fatal("start 不得等待 Ready")
	}
	m := decodeMap(t, body)
	instanceID = strField(m, "instance_id")
	gen = intField(m, "conn_generation")
	if instanceID == "" || gen == 0 {
		t.Fatalf("start 应返回 instance_id 与 conn_generation，body=%s", body)
	}
	return instanceID, gen
}

func (e *testEnv) waitReady(t *testing.T, id, instanceID string, gen int) {
	t.Helper()
	code, body := e.post(t, "/devices/"+id+"/wait_ready", map[string]any{
		"instance_id":     instanceID,
		"conn_generation": gen,
		"timeout_sec":     5,
	})
	if code != http.StatusOK {
		t.Fatalf("wait_ready 应 200，得到 %d body=%s", code, body)
	}
}

func (e *testEnv) createStartReady(t *testing.T, id string) (instanceID string, gen int) {
	t.Helper()
	e.createDevice(t, id)
	instanceID, gen = e.startDevice(t, id)
	e.waitReady(t, id, instanceID, gen)
	return instanceID, gen
}

func (e *testEnv) uploadWAV(t *testing.T) string {
	t.Helper()
	code, body := e.postAsset(t, "a.wav", wavPCM(16000))
	if code != http.StatusCreated {
		t.Fatalf("POST /assets 应 201，得到 %d body=%s", code, body)
	}
	id := strField(decodeMap(t, body), "asset_id")
	if id == "" {
		t.Fatalf("asset_id 为空，body=%s", body)
	}
	return id
}

func containsBytes(body []byte, s string) bool {
	return bytes.Contains(body, []byte(s))
}

func wsURL(httpURL, path, rawQuery string) string {
	u, err := url.Parse(httpURL)
	if err != nil {
		return ""
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path
	u.RawQuery = rawQuery
	return u.String()
}

func manageIsRegister(raw []byte) bool {
	if len(raw) == 0 || raw[0] != protocol.FirstManage {
		return false
	}
	env, err := protocol.DecodeManage(raw)
	if err != nil {
		return false
	}
	return len(env.Topic) >= len("/register/server") &&
		(env.Topic[len(env.Topic)-len("/register/server"):] == "/register/server")
}
