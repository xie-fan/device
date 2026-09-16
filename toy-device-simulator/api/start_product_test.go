package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startWithProduct 发一次 start：seed 的三级之上叠 extra（product / overrides）。
func (e *testEnv) startWithProduct(t *testing.T, id string, extra map[string]any) (int, map[string]any, []byte) {
	t.Helper()
	req := e.seedBinding()
	for k, v := range extra {
		req[k] = v
	}
	code, raw := e.post(t, "/devices/"+id+"/start", req)
	return code, decodeMap(t, raw), raw
}

func (e *testEnv) startReadyWithProduct(t *testing.T, id string, extra map[string]any) {
	t.Helper()
	code, m, raw := e.startWithProduct(t, id, extra)
	if code != http.StatusAccepted {
		t.Fatalf("start 应 202，得到 %d %s", code, raw)
	}
	e.waitReady(t, id, strField(m, "instance_id"), intField(m, "conn_generation"))
}

func (e *testEnv) stopForTest(t *testing.T, id string) {
	t.Helper()
	if code, raw := e.post(t, "/devices/"+id+"/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, raw)
	}
}

func (e *testEnv) configMap(t *testing.T, id string) map[string]any {
	t.Helper()
	code, raw, _ := e.get(t, "/devices/"+id+"/config")
	if code != http.StatusOK {
		t.Fatalf("GET config 应 200，得到 %d %s", code, raw)
	}
	return decodeMap(t, raw)
}

func (e *testEnv) deviceMap(t *testing.T, id string) map[string]any {
	t.Helper()
	code, raw, _ := e.get(t, "/devices/"+id)
	if code != http.StatusOK {
		t.Fatalf("GET device 应 200，得到 %d %s", code, raw)
	}
	return decodeMap(t, raw)
}

// mapAt 沿着键一路取嵌套对象；中途缺了返回 nil。
func mapAt(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m, _ = m[k].(map[string]any)
	}
	return m
}

func overridesOf(m map[string]any) map[string]any {
	ov, _ := m["overrides"].(map[string]any)
	return ov
}

func TestStartUsesDeviceTypeDefaultProduct(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_dp")
	code, m, raw := e.startWithProduct(t, "sim_dp", nil)
	if code != http.StatusAccepted {
		t.Fatalf("不给产品时用类型的默认产品，start 应 202，得到 %d %s", code, raw)
	}
	if strField(m, "product") != "test" {
		t.Fatalf("start 响应应带 product=test: %s", raw)
	}
	if strField(e.deviceMap(t, "sim_dp"), "product") != "test" {
		t.Fatal("设备视图应带 product=test")
	}
	cfg := e.configMap(t, "sim_dp")
	if strField(cfg, "product") != "test" || strField(mapAt(cfg, "audio"), "format") != "pcm" || strField(cfg, "nic_iccid") != "8986" {
		t.Fatalf("当前值应来自测试产品: %v", cfg)
	}
}

func TestStartExplicitProduct(t *testing.T) {
	e := newEnv(t)
	e.postProduct(t, amrProductBody("mh8w"))
	e.createDevice(t, "sim_ep")
	code, m, raw := e.startWithProduct(t, "sim_ep", map[string]any{"product": "mh8w"})
	if code != http.StatusAccepted || strField(m, "product") != "mh8w" {
		t.Fatalf("显式给的产品优先于类型默认产品，得到 %d %s", code, raw)
	}
	if got := strField(mapAt(e.configMap(t, "sim_ep"), "audio"), "format"); got != "amr" {
		t.Fatalf("音频应来自 mh8w 的 amr，得到 %s", got)
	}
}

func TestStartProductErrors(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_pe")
	if code, _, raw := e.startWithProduct(t, "sim_pe", map[string]any{"product": "nope"}); code != http.StatusNotFound {
		t.Fatalf("指定的产品不存在应 404，得到 %d %s", code, raw)
	}
	if code, raw := e.post(t, "/registry/environments/local/enterprises/demo/device_types", map[string]any{
		"name": "无默认", "short_name": "NOPROD",
	}); code != http.StatusCreated {
		t.Fatalf("建类型应 201，得到 %d %s", code, raw)
	}
	if code, raw := e.post(t, "/devices/sim_pe/start", map[string]any{
		"environment": "local", "enterprise": "demo", "device_type": "NOPROD",
	}); code != http.StatusBadRequest {
		t.Fatalf("不给产品且类型没有默认产品应 400，得到 %d %s", code, raw)
	}
	if st := strField(e.deviceMap(t, "sim_pe"), "instance_state"); st != "created" {
		t.Fatalf("失败的 start 不得改状态，得到 %s", st)
	}

	// 手改配置树让默认产品悬空（删除产品本身有引用守卫，走不到这一步）。
	e.srv.Close()
	tree, err := os.ReadFile(e.registry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tree), "default_product: test") {
		t.Fatalf("seed 的配置树应落盘 default_product: test: %s", tree)
	}
	dangling := strings.Replace(string(tree), "default_product: test", "default_product: ghost", 1)
	if err := os.WriteFile(e.registry, []byte(dangling), 0o644); err != nil {
		t.Fatal(err)
	}
	e.start(t, 0)
	if code, _, raw := e.startWithProduct(t, "sim_pe", nil); code != http.StatusBadRequest {
		t.Fatalf("默认产品悬空应 400，得到 %d %s", code, raw)
	}
}

func TestStartOverridesApplyAndShow(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_ov")
	code, _, raw := e.startWithProduct(t, "sim_ov", map[string]any{
		"overrides": map[string]any{
			"behavior.first_reply_timeout_sec": 7,
			"audio.format":                     "wav",
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("带覆盖 start 应 202，得到 %d %s", code, raw)
	}
	cfg := e.configMap(t, "sim_ov")
	if intField(mapAt(cfg, "behavior"), "first_reply_timeout_sec") != 7 || strField(mapAt(cfg, "audio"), "format") != "wav" {
		t.Fatalf("覆盖应进当前值: %v", cfg)
	}
	if !boolField(cfg, "overridden") {
		t.Fatalf("有覆盖时 overridden 应为 true: %v", cfg)
	}
	ov := overridesOf(cfg)
	if len(ov) != 2 || ov["audio.format"] != "wav" || ov["behavior.first_reply_timeout_sec"] != float64(7) {
		t.Fatalf("config 的 overrides 应恰好是这两项，得到 %v", ov)
	}
	dev := e.deviceMap(t, "sim_ov")
	if !boolField(dev, "overridden") || len(overridesOf(dev)) != 2 {
		t.Fatalf("设备视图也应带 overrides: %v", dev)
	}
}

// 覆盖不受产品清单限制：清单只决定默认值和界面推荐项。
func TestStartOverrideOutsideProductListAllowed(t *testing.T) {
	e := newEnv(t)
	e.postProduct(t, amrProductBody("mh8w"))
	e.createDevice(t, "sim_out")
	code, _, raw := e.startWithProduct(t, "sim_out", map[string]any{
		"product":   "mh8w",
		"overrides": map[string]any{"audio.format": "pcm"},
	})
	if code != http.StatusAccepted {
		t.Fatalf("清单外的覆盖应照常启动，得到 %d %s", code, raw)
	}
	if got := strField(mapAt(e.configMap(t, "sim_out"), "audio"), "format"); got != "pcm" {
		t.Fatalf("当前值应是覆盖后的 pcm，得到 %s", got)
	}
}

func TestStartOverridesRejectedAtomically(t *testing.T) {
	e := newEnv(t)
	e.postProduct(t, amrProductBody("mh8w"))
	e.createDevice(t, "sim_bad")
	e.startReadyWithProduct(t, "sim_bad", map[string]any{
		"overrides": map[string]any{"behavior.first_reply_timeout_sec": 7},
	})
	e.stopForTest(t, "sim_bad")

	bad := []map[string]any{
		{"playing_mode": 9},
		{"nope.x": 1},
		{"audio.nope": 1},
		{"device_id": "x"},
		{"enterprise": "x"},
		{"server.url": "ws://127.0.0.1:1/"},
		{"product": "mh8w"},
		{"behavior.write_queue_depth": 8},
		{"behavior.auto_register": false},
	}
	for _, ov := range bad {
		if code, _, raw := e.startWithProduct(t, "sim_bad", map[string]any{"overrides": ov}); code != http.StatusBadRequest {
			t.Errorf("覆盖 %v 应 400，得到 %d %s", ov, code, raw)
		}
	}
	// 换产品的同时带非法覆盖：产品也不能换过去。
	if code, _, raw := e.startWithProduct(t, "sim_bad", map[string]any{
		"product": "mh8w", "overrides": map[string]any{"playing_mode": 9},
	}); code != http.StatusBadRequest {
		t.Fatalf("非法覆盖应 400，得到 %d %s", code, raw)
	}
	dev := e.deviceMap(t, "sim_bad")
	if strField(dev, "instance_state") != "stopped" || strField(dev, "product") != "test" {
		t.Fatalf("失败的 start 不得改状态或产品: %v", dev)
	}
	if ov := overridesOf(dev); len(ov) != 1 || ov["behavior.first_reply_timeout_sec"] != float64(7) {
		t.Fatalf("失败的 start 不得动已有覆盖，得到 %v", ov)
	}
}

func TestOverridesKeptOnSameProductClearedOnSwitch(t *testing.T) {
	e := newEnv(t)
	e.postProduct(t, amrProductBody("mh8w"))
	e.createDevice(t, "sim_keep")
	e.startReadyWithProduct(t, "sim_keep", map[string]any{
		"overrides": map[string]any{"behavior.first_reply_timeout_sec": 7},
	})
	e.stopForTest(t, "sim_keep")

	// 同一产品再起：覆盖保留。
	e.startReadyWithProduct(t, "sim_keep", nil)
	cfg := e.configMap(t, "sim_keep")
	if intField(mapAt(cfg, "behavior"), "first_reply_timeout_sec") != 7 || len(overridesOf(cfg)) != 1 {
		t.Fatalf("同产品 stop/start 应保留覆盖: %v", cfg)
	}
	e.stopForTest(t, "sim_keep")

	// 换产品：覆盖全部清空。
	e.startReadyWithProduct(t, "sim_keep", map[string]any{"product": "mh8w"})
	cfg = e.configMap(t, "sim_keep")
	if strField(cfg, "product") != "mh8w" || len(overridesOf(cfg)) != 0 || boolField(cfg, "overridden") {
		t.Fatalf("换产品应清空覆盖: %v", cfg)
	}
	if intField(mapAt(cfg, "behavior"), "first_reply_timeout_sec") != 20 || strField(mapAt(cfg, "audio"), "format") != "amr" {
		t.Fatalf("换产品后应是 mh8w 的默认值: %v", cfg)
	}
}

func TestOverrideEqualToProductDefaultNotCounted(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_eq")
	e.startReadyWithProduct(t, "sim_eq", map[string]any{
		"overrides": map[string]any{
			"behavior.first_reply_timeout_sec": 20,
			"audio.format":                     "pcm",
		},
	})
	cfg := e.configMap(t, "sim_eq")
	if boolField(cfg, "overridden") || len(overridesOf(cfg)) != 0 {
		t.Fatalf("与产品默认值相同的字段不计入覆盖: %v", cfg)
	}
}

func TestPutConfigWritesOverridesAndResetRestores(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_put12")
	if code, raw := e.put(t, "/devices/sim_put12/config", map[string]any{
		"behavior": map[string]any{"first_reply_timeout_sec": 9},
	}); code != http.StatusConflict {
		t.Fatalf("没选过产品时 PUT /config 应 409，得到 %d %s", code, raw)
	}
	e.startReadyWithProduct(t, "sim_put12", nil)
	e.stopForTest(t, "sim_put12")

	code, raw := e.put(t, "/devices/sim_put12/config", map[string]any{
		"behavior": map[string]any{"first_reply_timeout_sec": 9},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /config 应 200，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if ov := overridesOf(m); ov["behavior.first_reply_timeout_sec"] != float64(9) || !boolField(m, "overridden") {
		t.Fatalf("PUT 应按点路径记进覆盖: %s", raw)
	}
	if code, raw := e.put(t, "/devices/sim_put12/config", map[string]any{"product": "test"}); code != http.StatusBadRequest {
		t.Fatalf("PUT /config 带 product 应 400，得到 %d %s", code, raw)
	}

	if code, raw := e.post(t, "/devices/sim_put12/config/reset", nil); code != http.StatusOK {
		t.Fatalf("reset 应 200，得到 %d %s", code, raw)
	}
	cfg := e.configMap(t, "sim_put12")
	if boolField(cfg, "overridden") || len(overridesOf(cfg)) != 0 || intField(mapAt(cfg, "behavior"), "first_reply_timeout_sec") != 20 {
		t.Fatalf("reset 应清空覆盖、回到产品默认值: %v", cfg)
	}
}

func TestPutFeaturesWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_feat")
	e.startReadyWithProduct(t, "sim_feat", nil)
	if code, raw := e.put(t, "/devices/sim_feat/config", map[string]any{
		"features": map[string]any{"photo": map[string]any{"enabled": true}},
	}); code != http.StatusOK {
		t.Fatalf("运行中改功能开关应 200，得到 %d %s", code, raw)
	}
	cfg := e.configMap(t, "sim_feat")
	if !boolField(mapAt(cfg, "features", "photo"), "enabled") {
		t.Fatalf("功能开关应已生效: %v", cfg)
	}
	if overridesOf(cfg)["features.photo.enabled"] != true {
		t.Fatalf("功能开关应记进覆盖: %v", cfg)
	}
	if code, raw := e.put(t, "/devices/sim_feat/config", map[string]any{
		"audio": map[string]any{"format": "wav"},
	}); code != http.StatusConflict {
		t.Fatalf("运行中改音频仍应 409，得到 %d %s", code, raw)
	}
}

func TestProductEditAppliesOnNextStart(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_pedit")
	e.startReadyWithProduct(t, "sim_pedit", nil)
	e.stopForTest(t, "sim_pedit")
	body := e.testProductBody()
	body["defaults"].(map[string]any)["behavior"].(map[string]any)["first_reply_timeout_sec"] = 11
	if code, raw := e.put(t, "/products/test", body); code != http.StatusOK {
		t.Fatalf("PUT /products/test 应 200，得到 %d %s", code, raw)
	}
	e.startReadyWithProduct(t, "sim_pedit", nil)
	if got := intField(mapAt(e.configMap(t, "sim_pedit"), "behavior"), "first_reply_timeout_sec"); got != 11 {
		t.Fatalf("产品改了，下次 start 应按新版本合成，得到 %d", got)
	}
}

func TestOverridesNotPersistedAcrossRestart(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_np")
	e.startReadyWithProduct(t, "sim_np", map[string]any{
		"overrides": map[string]any{"behavior.first_reply_timeout_sec": 7},
	})
	e.srv.Close()
	e.start(t, 0)
	dev := e.deviceMap(t, "sim_np")
	if strField(dev, "instance_state") != "created" || strField(dev, "product") != "" || len(overridesOf(dev)) != 0 || boolField(dev, "overridden") {
		t.Fatalf("重启后设备只剩 id：没有产品、没有覆盖，得到 %v", dev)
	}
}

func TestBatchStartCarriesProductAndOverrides(t *testing.T) {
	e := newEnv(t)
	e.postProduct(t, amrProductBody("mh8w"))
	e.createDevice(t, "sim_bp1")
	e.createDevice(t, "sim_bp2")
	req := e.seedBinding()
	req["device_ids"] = []string{"sim_bp1", "sim_bp2"}
	req["product"] = "mh8w"
	req["overrides"] = map[string]any{"behavior.first_reply_timeout_sec": 8}
	if code, raw := e.post(t, "/devices/batch/start", req); code != http.StatusAccepted {
		t.Fatalf("批量 start 应 202，得到 %d %s", code, raw)
	}
	for _, id := range []string{"sim_bp1", "sim_bp2"} {
		dev := e.deviceMap(t, id)
		if strField(dev, "product") != "mh8w" || overridesOf(dev)["behavior.first_reply_timeout_sec"] != float64(8) {
			t.Fatalf("%s 应用上整批的产品与覆盖: %v", id, dev)
		}
	}
}

func TestTurnRecordsProductAndOverrides(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	ins, turnID := runOneTurn(t, e, "sim_trec")
	// 重启顺带把 recorder 排干，turn.json 一定落到位。
	e.srv.Close()
	e.start(t, 0)

	raw, err := os.ReadFile(filepath.Join(e.recDir, "sim_trec", ins, turnID, "turn.json"))
	if err != nil {
		t.Fatalf("读 turn.json：%v", err)
	}
	var row struct {
		Product   string         `json:"product"`
		Overrides map[string]any `json:"overrides"`
	}
	if err := json.Unmarshal(trimLastLine(raw), &row); err != nil {
		t.Fatalf("解 turn.json：%v", err)
	}
	if row.Product != "test" {
		t.Fatalf("turn.json 应记下产品 test，得到 %+v", row)
	}
	if row.Overrides["behavior.downlink_idle_timeout_sec"] != float64(1) || row.Overrides["behavior.first_reply_timeout_sec"] != float64(2) {
		t.Fatalf("turn.json 应记下本轮生效的覆盖，得到 %+v", row)
	}

	code, body, _ := e.get(t, "/devices/sim_trec/instances")
	if code != http.StatusOK {
		t.Fatalf("GET instances 应 200，得到 %d %s", code, body)
	}
	for _, it := range listField(t, body, "instances") {
		m, _ := it.(map[string]any)
		if strField(m, "instance_id") != ins {
			continue
		}
		if strField(m, "product") != "test" {
			t.Fatalf("instances 应带 product: %s", body)
		}
		return
	}
	t.Fatalf("instances 里没有 %s: %s", ins, body)
}
