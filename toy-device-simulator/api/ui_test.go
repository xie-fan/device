package api

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestUIIndexAndAssets(t *testing.T) {
	e := newEnv(t)

	code, body, hdr := e.get(t, "/")
	if code != http.StatusOK {
		t.Fatalf("GET / 应 200，得到 %d", code)
	}
	ct := hdr.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / Content-Type 应含 text/html，得到 %q", ct)
	}
	html := string(body)
	if !strings.Contains(html, "台架") {
		t.Fatalf("GET / 应含调试台标题")
	}
	if !strings.Contains(html, `id="filter-enterprise"`) || !strings.Contains(html, `id="filter-type"`) {
		t.Fatalf("GET / 应含厂商与设备类型筛选")
	}
	// 重设计后批量启停删回到左栏（勾选设备才出现），走 /devices/batch/*。
	for _, id := range []string{"btn-batch-start", "btn-batch-stop", "btn-batch-delete"} {
		if !strings.Contains(html, id) {
			t.Fatalf("左栏应有批量条 %s", id)
		}
	}
	// 支撑面板（新建 / 配置 / 音频库 / 模板 / 故障 / 术语 / 场景）都退到抽屉里。
	if !strings.Contains(html, `id="drawer-body"`) || !strings.Contains(html, `id="btn-new"`) {
		t.Fatalf("新建与配置等支撑面板应收进抽屉")
	}
	// 中栏是真的对话流，不是控制面板。
	if !strings.Contains(html, `id="conv"`) || !strings.Contains(html, `class="sendbar"`) {
		t.Fatalf("中栏应有对话流与单条送话条")
	}
	if !strings.Contains(html, "/ui/app.js") || !strings.Contains(html, "/ui/app.css") {
		t.Fatalf("GET / 应引用 /ui/app.js 与 /ui/app.css")
	}

	code, body, hdr = e.get(t, "/ui/app.js")
	if code != http.StatusOK {
		t.Fatalf("GET /ui/app.js 应 200，得到 %d", code)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "javascript") {
		t.Fatalf("app.js Content-Type 应含 javascript，得到 %q", hdr.Get("Content-Type"))
	}
	js := string(body)
	if !strings.Contains(js, "turn_terminal") || !strings.Contains(js, "wait_ready") {
		t.Fatalf("app.js 应消费 turn_terminal / wait_ready")
	}
	if !strings.Contains(js, "after_event_seq") {
		t.Fatalf("app.js 应处理 after_event_seq 游标")
	}
	if !strings.Contains(js, "filterEnterprise") || !strings.Contains(js, "/frames") {
		t.Fatalf("app.js 应含厂商筛选与 frames 时间轴")
	}
	if !strings.Contains(js, "payload_len") || !strings.Contains(js, "上传") || !strings.Contains(js, "下发") {
		t.Fatalf("app.js 事件带应合并上传/下发并展示包大小")
	}
	// 送出区任何时候都在，只由 blockedReason 决定按钮能不能点并给出理由。
	if !strings.Contains(js, "function blockedReason") {
		t.Fatalf("送话可用性应收敛到 blockedReason 一个真源")
	}
	if strings.Contains(js, `show($("form-speak")`) {
		t.Fatalf("送出区不得在非 running 时整块关掉")
	}
	// 后端早有、旧页面没用上的能力：批量、上行回放、同步送话。
	for _, needle := range []string{"/devices/batch/", "audio/${dir}", "speak_and_wait"} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app.js 应接上 %s", needle)
		}
	}
	if !strings.Contains(js, "可再送出") {
		t.Fatalf("turn_terminal 后应提示可再送出")
	}

	code, body, hdr = e.get(t, "/ui/app.css")
	if code != http.StatusOK {
		t.Fatalf("GET /ui/app.css 应 200，得到 %d", code)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/css") {
		t.Fatalf("app.css Content-Type 应含 text/css，得到 %q", hdr.Get("Content-Type"))
	}
	if len(body) == 0 {
		t.Fatalf("app.css 不应为空")
	}

	code, _, _ = e.get(t, "/ui/secret.txt")
	if code != http.StatusNotFound {
		t.Fatalf("未登记的 UI 文件应 404，得到 %d", code)
	}
}

func TestUIDoesNotShadowPhase2API(t *testing.T) {
	e := newEnv(t)
	code, body := e.post(t, "/devices", e.createBody(e.deviceBody("sim_ui_1")))
	if code != http.StatusCreated {
		t.Fatalf("POST /devices 应 201，得到 %d body=%s", code, body)
	}
	code, body, _ = e.get(t, "/devices")
	if code != http.StatusOK {
		t.Fatalf("GET /devices 应 200，得到 %d body=%s", code, body)
	}
	if !strings.Contains(string(body), "sim_ui_1") {
		t.Fatalf("GET /devices 应列出 sim_ui_1，body=%s", body)
	}
	code, ast := e.postAsset(t, "hello.wav", wavPCM(16000))
	if code != http.StatusCreated {
		t.Fatalf("POST /assets 应 201，得到 %d body=%s", code, ast)
	}
}

func TestListSamplesIncludesWAV(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.get(t, "/samples")
	if code != http.StatusOK {
		t.Fatalf("GET /samples 应 200，得到 %d body=%s", code, body)
	}
	if !strings.Contains(string(body), "hello.wav") {
		t.Fatalf("GET /samples 应含 hello.wav，body=%s", body)
	}
}

func TestSampleHelloWAVFromTestdata(t *testing.T) {
	e := newEnv(t)
	code, body, hdr := e.get(t, "/samples/hello.wav")
	if code != http.StatusOK {
		t.Fatalf("GET /samples/hello.wav 应 200（testdata/hello.wav），得到 %d", code)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "audio/wav") && !strings.Contains(hdr.Get("Content-Type"), "wav") {
		t.Fatalf("hello.wav Content-Type 应含 wav，得到 %q", hdr.Get("Content-Type"))
	}
	if !bytes.HasPrefix(body, []byte("RIFF")) {
		t.Fatalf("testdata/hello.wav 应为 RIFF WAV")
	}
	code, ast := e.postAsset(t, "hello.wav", body)
	if code != http.StatusCreated {
		t.Fatalf("仓库 hello.wav POST /assets 应 201，得到 %d body=%s", code, ast)
	}
}
