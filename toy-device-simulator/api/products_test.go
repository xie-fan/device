package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// amrProductBody 最小可用的产品：只给清单和 amr 默认值，其余属性由服务端按内置基线补齐。
func amrProductBody(id string) map[string]any {
	return map[string]any{
		"id":            id,
		"name":          "产品 " + id,
		"playing_modes": []int{1, 3},
		"audio_formats": []string{"amr/16000"},
		"defaults": map[string]any{
			"audio": map[string]any{"format": "amr", "sample_rate": 16000},
		},
	}
}

func (e *testEnv) postProduct(t *testing.T, body map[string]any) {
	t.Helper()
	if code, raw := e.post(t, "/products", body); code != http.StatusCreated {
		t.Fatalf("POST /products 应 201，得到 %d %s", code, raw)
	}
}

func productIDsOf(t *testing.T, body []byte) []string {
	t.Helper()
	list, _ := decodeMap(t, body)["products"].([]any)
	ids := make([]string, 0, len(list))
	for _, row := range list {
		m, _ := row.(map[string]any)
		ids = append(ids, strField(m, "id"))
	}
	return ids
}

func TestProductsCRUD(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.get(t, "/products")
	if code != http.StatusOK {
		t.Fatalf("GET /products 应 200，得到 %d %s", code, body)
	}
	if ids := strings.Join(productIDsOf(t, body), ","); ids != "default,test" {
		t.Fatalf("应有自动生成的 default 与 seed 的 test（按 id 排序），得到 %s", ids)
	}

	code, raw := e.post(t, "/products", amrProductBody("mh8w"))
	if code != http.StatusCreated {
		t.Fatalf("POST 应 201，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if strField(m, "id") != "mh8w" {
		t.Fatalf("响应应带 id: %s", raw)
	}
	def, _ := m["defaults"].(map[string]any)
	audio, _ := def["audio"].(map[string]any)
	beh, _ := def["behavior"].(map[string]any)
	if strField(audio, "format") != "amr" || intField(audio, "sample_rate") != 16000 {
		t.Fatalf("默认音频应是 amr/16000: %s", raw)
	}
	if intField(beh, "first_reply_timeout_sec") != 20 || strField(def, "nic_type") != "wifi" {
		t.Fatalf("省略的属性应按内置基线补齐: %s", raw)
	}
	feat, _ := def["features"].(map[string]any)
	photo, _ := feat["photo"].(map[string]any)
	if photo == nil || boolField(photo, "enabled") {
		t.Fatalf("defaults 应带 features.photo 且默认关闭: %s", raw)
	}

	if code, raw := e.post(t, "/products", amrProductBody("mh8w")); code != http.StatusConflict {
		t.Fatalf("重复 id 应 409，得到 %d %s", code, raw)
	}
	if code, raw, _ := e.get(t, "/products/mh8w"); code != http.StatusOK || strField(decodeMap(t, raw), "name") != "产品 mh8w" {
		t.Fatalf("GET 单个产品应 200，得到 %d %s", code, raw)
	}
	if code, raw, _ := e.get(t, "/products/nope"); code != http.StatusNotFound {
		t.Fatalf("不存在的产品应 404，得到 %d %s", code, raw)
	}

	upd := amrProductBody("mh8w")
	delete(upd, "id")
	upd["name"] = "MH8W 二代"
	upd["defaults"] = map[string]any{
		"playing_mode": 3,
		"audio":        map[string]any{"format": "amr", "sample_rate": 16000},
	}
	if code, raw := e.put(t, "/products/mh8w", upd); code != http.StatusOK {
		t.Fatalf("PUT 应 200，得到 %d %s", code, raw)
	}
	_, raw, _ = e.get(t, "/products/mh8w")
	m = decodeMap(t, raw)
	def, _ = m["defaults"].(map[string]any)
	if strField(m, "name") != "MH8W 二代" || intField(def, "playing_mode") != 3 {
		t.Fatalf("PUT 后应生效: %s", raw)
	}
	if code, raw := e.put(t, "/products/mh8w", amrProductBody("other")); code != http.StatusBadRequest {
		t.Fatalf("body 的 id 与路径不符应 400，得到 %d %s", code, raw)
	}
	if code, raw := e.put(t, "/products/nope", amrProductBody("nope")); code != http.StatusNotFound {
		t.Fatalf("PUT 不存在的产品应 404，得到 %d %s", code, raw)
	}

	if code, raw := e.del(t, "/products/mh8w"); code != http.StatusNoContent {
		t.Fatalf("DELETE 应 204，得到 %d %s", code, raw)
	}
	if code, _, _ := e.get(t, "/products/mh8w"); code != http.StatusNotFound {
		t.Fatalf("删除后应 404，得到 %d", code)
	}
	if code, raw := e.del(t, "/products/mh8w"); code != http.StatusNotFound {
		t.Fatalf("再删应 404，得到 %d %s", code, raw)
	}
}

func TestProductValidation400(t *testing.T) {
	e := newEnv(t)
	defaults := func(b map[string]any) map[string]any { return b["defaults"].(map[string]any) }
	cases := []struct {
		name string
		mut  func(b map[string]any)
	}{
		{"缺 id", func(b map[string]any) { delete(b, "id") }},
		{"id 带斜杠", func(b map[string]any) { b["id"] = "a/b" }},
		{"名称为空", func(b map[string]any) { b["name"] = "" }},
		{"对话模式清单为空", func(b map[string]any) { b["playing_modes"] = []int{} }},
		{"不支持的音频格式", func(b map[string]any) { b["audio_formats"] = []string{"flac/16000"} }},
		{"默认对话模式不在清单内", func(b map[string]any) { b["playing_modes"] = []int{2} }},
		{"defaults 未知字段", func(b map[string]any) { defaults(b)["nope"] = 1 }},
		{"defaults 带 device_id", func(b map[string]any) { defaults(b)["device_id"] = "x" }},
		{"defaults 带 enterprise", func(b map[string]any) { defaults(b)["enterprise"] = "x" }},
		{"defaults 带 server", func(b map[string]any) {
			defaults(b)["server"] = map[string]any{"url": "ws://127.0.0.1:1/"}
		}},
		{"defaults 带 write_queue_depth", func(b map[string]any) {
			defaults(b)["behavior"] = map[string]any{"write_queue_depth": 8}
		}},
		{"defaults 的 auto_register=false", func(b map[string]any) {
			defaults(b)["behavior"] = map[string]any{"auto_register": false}
		}},
	}
	for _, c := range cases {
		b := amrProductBody("bad")
		c.mut(b)
		if code, raw := e.post(t, "/products", b); code != http.StatusBadRequest {
			t.Errorf("%s 应 400，得到 %d %s", c.name, code, raw)
		}
	}
	if code, _, _ := e.get(t, "/products/bad"); code != http.StatusNotFound {
		t.Fatalf("校验失败的产品不得入库，GET 得到 %d", code)
	}
}

func TestProductsSeedFileAndPersistAcrossRestart(t *testing.T) {
	e := newEnv(t)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(e.cfg.AssetsRoot), "products.yaml"))
	if err != nil {
		t.Fatalf("products.yaml 应与设备册同在 assets_root 的父目录: %v", err)
	}
	if !strings.Contains(string(raw), "id: default") {
		t.Fatalf("缺文件时应自动写入默认产品: %s", raw)
	}
	e.postProduct(t, amrProductBody("mh8w"))
	e.srv.Close()
	e.start(t, 0)
	if code, body, _ := e.get(t, "/products/mh8w"); code != http.StatusOK {
		t.Fatalf("重启后产品应还在，得到 %d %s", code, body)
	}
}

func TestDeviceTypeDefaultProduct(t *testing.T) {
	e := newEnv(t)
	typePath := "/registry/environments/local/enterprises/demo/device_types/A3"
	if _, raw, _ := e.get(t, "/registry"); !containsBytes(raw, `"default_product":"test"`) {
		t.Fatalf("seed 的 A3 应带默认产品 test: %s", raw)
	}
	if code, raw := e.put(t, typePath, map[string]any{"name": "A3 音箱", "default_product": "nope"}); code != http.StatusNotFound {
		t.Fatalf("默认产品不存在应 404，得到 %d %s", code, raw)
	}
	e.postProduct(t, amrProductBody("mh8w"))
	if code, raw := e.put(t, typePath, map[string]any{"name": "A3 音箱", "default_product": "mh8w"}); code != http.StatusOK {
		t.Fatalf("设默认产品应 200，得到 %d %s", code, raw)
	}
	// 不带 default_product 的 PUT 不动它。
	if code, raw := e.put(t, typePath, map[string]any{"name": "A3 二代"}); code != http.StatusOK {
		t.Fatalf("改名应 200，得到 %d %s", code, raw)
	}
	if _, raw, _ := e.get(t, "/registry"); !containsBytes(raw, `"default_product":"mh8w"`) {
		t.Fatalf("改名不应丢默认产品: %s", raw)
	}
	if code, raw := e.put(t, typePath, map[string]any{"name": "A3 二代", "default_product": ""}); code != http.StatusOK {
		t.Fatalf("清空默认产品应 200，得到 %d %s", code, raw)
	}
	if _, raw, _ := e.get(t, "/registry"); containsBytes(raw, `"default_product":"mh8w"`) {
		t.Fatalf("清空后不应还是 mh8w: %s", raw)
	}

	// 新建类型时也能带；产品不存在则不建。
	typesPath := "/registry/environments/local/enterprises/demo/device_types"
	if code, raw := e.post(t, typesPath, map[string]any{"name": "X1", "short_name": "X1", "default_product": "nope"}); code != http.StatusNotFound {
		t.Fatalf("新建类型带不存在的默认产品应 404，得到 %d %s", code, raw)
	}
	if code, raw := e.post(t, typesPath, map[string]any{"name": "X2", "short_name": "X2", "default_product": "mh8w"}); code != http.StatusCreated {
		t.Fatalf("新建类型带默认产品应 201，得到 %d %s", code, raw)
	}
	_, raw, _ := e.get(t, "/registry")
	if containsBytes(raw, `"short_name":"X1"`) {
		t.Fatalf("默认产品不存在时不应建出类型: %s", raw)
	}
	if !containsBytes(raw, `"default_product":"mh8w"`) {
		t.Fatalf("X2 应带默认产品 mh8w: %s", raw)
	}
}

func TestProductDeleteGuards(t *testing.T) {
	e := newEnv(t)
	if code, raw := e.del(t, "/products/test"); code != http.StatusConflict {
		t.Fatalf("被设备类型设为默认产品时删除应 409，得到 %d %s", code, raw)
	}
	e.postProduct(t, amrProductBody("mh8w"))
	e.createDevice(t, "sim_pg")
	req := e.seedBinding()
	req["product"] = "mh8w"
	code, raw := e.post(t, "/devices/sim_pg/start", req)
	if code != http.StatusAccepted {
		t.Fatalf("start 应 202，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	e.waitReady(t, "sim_pg", strField(m, "instance_id"), intField(m, "conn_generation"))
	if code, raw := e.del(t, "/products/mh8w"); code != http.StatusConflict {
		t.Fatalf("正被运行中的设备使用时删除应 409，得到 %d %s", code, raw)
	}
	if code, raw := e.post(t, "/devices/sim_pg/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, raw)
	}
	if code, raw := e.del(t, "/products/mh8w"); code != http.StatusNoContent {
		t.Fatalf("停下后删除应 204，得到 %d %s", code, raw)
	}
}
