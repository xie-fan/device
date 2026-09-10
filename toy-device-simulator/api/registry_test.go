package api

import (
	"net/http"
	"testing"
)

// seed（helpers_test.go）已建 local 环境 + demo 厂商 + A3 类型。

func TestRegistryTreeCRUD(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.get(t, "/registry")
	if code != http.StatusOK {
		t.Fatalf("GET /registry 应 200，得到 %d %s", code, body)
	}
	m := decodeMap(t, body)
	envs, _ := m["environments"].([]any)
	if len(envs) != 1 {
		t.Fatalf("应有 1 个环境，body=%s", body)
	}
	env, _ := envs[0].(map[string]any)
	if strField(env, "name") != "local" || strField(env, "url") != "ws://127.0.0.1:1/" {
		t.Fatalf("环境字段不符: %s", body)
	}
	ents, _ := env["enterprises"].([]any)
	if len(ents) != 1 {
		t.Fatalf("应有 1 个厂商: %s", body)
	}
	ent, _ := ents[0].(map[string]any)
	if strField(ent, "name") != "演示厂商" || strField(ent, "short_name") != "demo" {
		t.Fatalf("厂商名称/简称不符: %s", body)
	}
	typs, _ := ent["device_types"].([]any)
	if len(typs) != 1 {
		t.Fatalf("应有 1 个类型: %s", body)
	}
	typ, _ := typs[0].(map[string]any)
	if strField(typ, "name") != "A3 音箱" || strField(typ, "short_name") != "A3" {
		t.Fatalf("类型名称/简称不符: %s", body)
	}

	// 重名 409；父缺失 404。
	if code, body := e.post(t, "/registry/environments", map[string]any{"name": "local", "url": "ws://x/"}); code != http.StatusConflict {
		t.Fatalf("环境重名应 409，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises", map[string]any{"name": "又一个", "short_name": "demo"}); code != http.StatusConflict {
		t.Fatalf("厂商简称重名应 409，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/nope/enterprises", map[string]any{"name": "x", "short_name": "x"}); code != http.StatusNotFound {
		t.Fatalf("父环境缺失应 404，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises/nope/device_types", map[string]any{"name": "x", "short_name": "X1"}); code != http.StatusNotFound {
		t.Fatalf("父厂商缺失应 404，得到 %d %s", code, body)
	}

	// 展示名与环境 url 可改；键不可改（无该端点，改键 = 删掉重建）。
	if code, body := e.put(t, "/registry/environments/local", map[string]any{"url": "ws://127.0.0.1:2/"}); code != http.StatusOK {
		t.Fatalf("PUT 环境 url 应 200，得到 %d %s", code, body)
	}
	if code, body := e.put(t, "/registry/environments/local/enterprises/demo", map[string]any{"name": "改名厂商"}); code != http.StatusOK {
		t.Fatalf("PUT 厂商名称应 200，得到 %d %s", code, body)
	}
	if code, body := e.put(t, "/registry/environments/local/enterprises/demo/device_types/A3", map[string]any{"name": "A3 二代"}); code != http.StatusOK {
		t.Fatalf("PUT 类型名称应 200，得到 %d %s", code, body)
	}
	_, body, _ = e.get(t, "/registry")
	if !containsBytes(body, "改名厂商") || !containsBytes(body, "A3 二代") || !containsBytes(body, "ws://127.0.0.1:2/") {
		t.Fatalf("PUT 后 GET /registry 未反映: %s", body)
	}
}

func TestRegistryValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"未知占位符", map[string]any{"name": "e1", "url": "ws://h/{vendor}"}},
		{"非 ws scheme", map[string]any{"name": "e2", "url": "http://h/"}},
		{"缺 host", map[string]any{"name": "e3", "url": "ws:///path"}},
		{"环境名带斜杠", map[string]any{"name": "a/b", "url": "ws://h/"}},
	}
	for _, c := range cases {
		if code, body := e.post(t, "/registry/environments", c.body); code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，得到 %d %s", c.name, code, body)
		}
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises", map[string]any{
		"name": "坏简称", "short_name": "a/b",
	}); code != http.StatusBadRequest {
		t.Fatalf("厂商简称带 / 应 400，得到 %d %s", code, body)
	}
	// MH 机型可以建：拦的是在它上面跑 bad_seq，见 TestBadSeqRejectedOnMHDeviceType。
	if code, body := e.post(t, "/registry/environments/local/enterprises/demo/device_types", map[string]any{
		"name": "海思", "short_name": "MH1",
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("MH 类型应可建，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises", map[string]any{
		"name": "", "short_name": "ok1",
	}); code != http.StatusBadRequest {
		t.Fatalf("厂商名称为空应 400，得到 %d %s", code, body)
	}
}

func TestRegistryPersistsAcrossRestart(t *testing.T) {
	e := newEnv(t)
	if code, body := e.post(t, "/registry/environments", map[string]any{
		"name": "staging", "url": "ws://127.0.0.1:9/{enterprise}",
	}); code != http.StatusCreated {
		t.Fatalf("建 staging 应 201，得到 %d %s", code, body)
	}
	// 同一 registry 路径重建 Server：树来自落盘文件。
	e.start(t, 0)
	code, body, _ := e.get(t, "/registry")
	if code != http.StatusOK {
		t.Fatalf("重启后 GET /registry 应 200，得到 %d %s", code, body)
	}
	for _, want := range []string{"local", "demo", "A3", "staging", "ws://127.0.0.1:9/{enterprise}"} {
		if !containsBytes(body, want) {
			t.Fatalf("重启后应保留 %s: %s", want, body)
		}
	}
}

// Phase 11：建的是设备册条目，挂靠移到 start。三级出现在创建体里一律 400，
// 引用是否存在改由 start 校验。
func TestPostDevicesBookEntryRules(t *testing.T) {
	e := newEnv(t)
	// 不给三级 → 201。
	if code, body := e.post(t, "/devices", map[string]any{"device": e.deviceBody("sim_r0")}); code != http.StatusCreated {
		t.Fatalf("设备册条目应 201，得到 %d %s", code, body)
	}
	// 创建体带三级 → 400。
	for _, k := range []string{"environment", "enterprise", "device_type"} {
		bad := e.createBody(e.deviceBody("sim_r1"))
		bad[k] = "x"
		if code, body := e.post(t, "/devices", bad); code != http.StatusBadRequest {
			t.Fatalf("创建体带 %s 应 400，得到 %d %s", k, code, body)
		}
	}
	// 引用不存在在 start 时才发现 → 404。
	e.createDevice(t, "sim_r1")
	if code, body := e.post(t, "/devices/sim_r1/start", map[string]any{
		"environment": "nope", "enterprise": "demo", "device_type": "A3",
	}); code != http.StatusNotFound {
		t.Fatalf("start 环境缺失应 404，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/devices/sim_r1/start", map[string]any{
		"environment": "local", "enterprise": "demo", "device_type": "ZZ",
	}); code != http.StatusNotFound {
		t.Fatalf("start 类型缺失应 404，得到 %d %s", code, body)
	}
	// 设备体带身份三键 → 400。
	for _, k := range []string{"enterprise", "device_type", "server"} {
		dev := e.deviceBody("sim_r2")
		if k == "server" {
			dev[k] = map[string]any{"url": "ws://127.0.0.1:1/"}
		} else {
			dev[k] = "x"
		}
		if code, body := e.post(t, "/devices", e.createBody(dev)); code != http.StatusBadRequest {
			t.Fatalf("设备体带 %s 应 400，得到 %d %s", k, code, body)
		}
	}
	// 模板同禁。
	dev := e.deviceBody("ignored")
	delete(dev, "device_id")
	dev["server"] = map[string]any{"url": "ws://127.0.0.1:1/"}
	if code, body := e.postTemplate(t, "bad_srv", dev); code != http.StatusBadRequest {
		t.Fatalf("模板带 server 应 400，得到 %d %s", code, body)
	}
}

func TestURLPlaceholderSubstitution(t *testing.T) {
	e := newEnv(t)
	if code, body := e.post(t, "/registry/environments", map[string]any{
		"name": "tpl", "url": "ws://127.0.0.1:1/{enterprise}/{device_type}/{device_id}",
	}); code != http.StatusCreated {
		t.Fatalf("建 tpl 环境应 201，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/tpl/enterprises", map[string]any{
		"name": "威普", "short_name": "vp",
	}); code != http.StatusCreated {
		t.Fatalf("建 vp 厂商应 201，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/tpl/enterprises/vp/device_types", map[string]any{
		"name": "语音测试", "short_name": "VT",
	}); code != http.StatusCreated {
		t.Fatalf("建 VT 类型应 201，得到 %d %s", code, body)
	}
	e.createDevice(t, "sim_url1")
	// 占位符在 start 挂靠时代入。
	if code, raw := e.post(t, "/devices/sim_url1/start", map[string]any{
		"environment": "tpl", "enterprise": "vp", "device_type": "VT",
	}); code != http.StatusAccepted {
		t.Fatalf("start 应 202，得到 %d %s", code, raw)
	}
	_, cfgRaw, _ := e.get(t, "/devices/sim_url1/config")
	m := decodeMap(t, cfgRaw)
	server, _ := m["server"].(map[string]any)
	want := "ws://127.0.0.1:1/vp/VT/sim_url1"
	if strField(server, "url") != want {
		t.Fatalf("server.url 应为 %s，得到 %s", want, cfgRaw)
	}
	if strField(m, "enterprise") != "vp" || strField(m, "device_type") != "VT" || strField(m, "environment") != "tpl" {
		t.Fatalf("config 树字段不符: %s", cfgRaw)
	}
	// dial 收到的也是代入结果。
	_, gbody, _ := e.get(t, "/devices/sim_url1")
	gm := decodeMap(t, gbody)
	e.waitReady(t, "sim_url1", strField(gm, "instance_id"), intField(gm, "conn_generation"))
	if got := e.dialURL("sim_url1"); got != want {
		t.Fatalf("dial url 应为 %s，得到 %s", want, got)
	}
}

func TestRegistryDeleteGuards(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_ref")
	if code, body := e.del(t, "/registry/environments/local"); code != http.StatusConflict {
		t.Fatalf("环境下有厂商应 409，得到 %d %s", code, body)
	}
	if code, body := e.del(t, "/registry/environments/local/enterprises/demo"); code != http.StatusConflict {
		t.Fatalf("厂商下有类型应 409，得到 %d %s", code, body)
	}
	// Phase 11：设备册条目不引用任何类型，停着的设备挡不住删除。
	if code, body := e.del(t, "/registry/environments/local/enterprises/demo/device_types/A3"); code != http.StatusNoContent {
		t.Fatalf("停着的设备不该挡删类型，得到 %d %s", code, body)
	}
	// 重建类型，跑起来再删——运行中的才算引用。
	if code, body := e.post(t, "/registry/environments/local/enterprises/demo/device_types", map[string]any{
		"name": "A3 音箱", "short_name": "A3",
	}); code != http.StatusCreated {
		t.Fatalf("重建类型应 201，得到 %d %s", code, body)
	}
	ins, gen := e.startDevice(t, "sim_ref")
	e.waitReady(t, "sim_ref", ins, gen)
	if code, body := e.del(t, "/registry/environments/local/enterprises/demo/device_types/A3"); code != http.StatusConflict {
		t.Fatalf("类型正被运行中的设备使用应 409，得到 %d %s", code, body)
	}
	if code, body := e.del(t, "/devices/sim_ref"); code != http.StatusOK {
		t.Fatalf("删设备应 200，得到 %d %s", code, body)
	}
	// tombstone 不算引用；自底向上删干净。
	if code, body := e.del(t, "/registry/environments/local/enterprises/demo/device_types/A3"); code != http.StatusNoContent {
		t.Fatalf("无引用后删类型应 204，得到 %d %s", code, body)
	}
	if code, body := e.del(t, "/registry/environments/local/enterprises/demo"); code != http.StatusNoContent {
		t.Fatalf("删厂商应 204，得到 %d %s", code, body)
	}
	if code, body := e.del(t, "/registry/environments/local"); code != http.StatusNoContent {
		t.Fatalf("删环境应 204，得到 %d %s", code, body)
	}
	if code, body := e.del(t, "/registry/environments/local"); code != http.StatusNotFound {
		t.Fatalf("再删应 404，得到 %d %s", code, body)
	}
}

// Phase 11：挂靠归 start。同一台设备可以先挂 demo 跑一轮，停下来再挂 beta 跑，
// 设备册条目一个字没变。PUT /config 不再管挂靠。
func TestRebindOnStartNotPut(t *testing.T) {
	e := newEnv(t)
	if code, body := e.post(t, "/registry/environments/local/enterprises", map[string]any{
		"name": "贝塔", "short_name": "beta",
	}); code != http.StatusCreated {
		t.Fatalf("建 beta 厂商应 201，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/registry/environments/local/enterprises/beta/device_types", map[string]any{
		"name": "A3 音箱", "short_name": "A3",
	}); code != http.StatusCreated {
		t.Fatalf("建 beta/A3 类型应 201，得到 %d %s", code, body)
	}
	e.createDevice(t, "sim_att")

	// PUT /config 碰挂靠一律 400——改了也会被下次 start 覆盖，不给假成功。
	for _, k := range []string{"environment", "enterprise", "device_type"} {
		if code, body := e.put(t, "/devices/sim_att/config", map[string]any{k: "beta"}); code != http.StatusBadRequest {
			t.Fatalf("PUT /config 带 %s 应 400，得到 %d %s", k, code, body)
		}
	}

	// start 时挂 demo。
	ins, gen := e.startDevice(t, "sim_att")
	e.waitReady(t, "sim_att", ins, gen)
	_, body, _ := e.get(t, "/devices/sim_att")
	if strField(decodeMap(t, body), "enterprise") != "demo" {
		t.Fatalf("首次应挂 demo: %s", body)
	}
	if code, body := e.post(t, "/devices/sim_att/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, body)
	}

	// 同一台设备改挂 beta。
	if code, body := e.post(t, "/devices/sim_att/start", map[string]any{
		"environment": "local", "enterprise": "beta", "device_type": "A3",
	}); code != http.StatusAccepted {
		t.Fatalf("改挂 beta 应 202，得到 %d %s", code, body)
	}
	_, body, _ = e.get(t, "/devices/sim_att")
	if strField(decodeMap(t, body), "enterprise") != "beta" {
		t.Fatalf("改挂后应是 beta: %s", body)
	}

	// 挂靠引用缺失 → 404。
	if code, body := e.post(t, "/devices/sim_att/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/devices/sim_att/start", map[string]any{
		"environment": "local", "enterprise": "nope", "device_type": "A3",
	}); code != http.StatusNotFound {
		t.Fatalf("挂靠引用缺失应 404，得到 %d %s", code, body)
	}
	// 三级不全 → 400。
	if code, body := e.post(t, "/devices/sim_att/start", map[string]any{"environment": "local"}); code != http.StatusBadRequest {
		t.Fatalf("三级不全应 400，得到 %d %s", code, body)
	}
}

func TestEnvURLUpdateAppliesOnRestart(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_env")
	ins, gen := e.startDevice(t, "sim_env")
	e.waitReady(t, "sim_env", ins, gen)
	if got := e.dialURL("sim_env"); got != "ws://127.0.0.1:1/" {
		t.Fatalf("首次 dial url 应为种子地址，得到 %s", got)
	}
	if code, body := e.post(t, "/devices/sim_env/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, body)
	}
	if code, body := e.put(t, "/registry/environments/local", map[string]any{"url": "ws://127.0.0.1:7/"}); code != http.StatusOK {
		t.Fatalf("PUT 环境 url 应 200，得到 %d %s", code, body)
	}
	ins, gen = e.startDevice(t, "sim_env")
	e.waitReady(t, "sim_env", ins, gen)
	if got := e.dialURL("sim_env"); got != "ws://127.0.0.1:7/" {
		t.Fatalf("重启后 dial url 应为新环境地址，得到 %s", got)
	}
	_, cfgRaw, _ := e.get(t, "/devices/sim_env/config")
	server, _ := decodeMap(t, cfgRaw)["server"].(map[string]any)
	if strField(server, "url") != "ws://127.0.0.1:7/" {
		t.Fatalf("config server.url 应跟随环境更新: %s", cfgRaw)
	}
}

// MH 机型能建、能挂设备；只有 bad_seq 注入被拦——服务端的 Seq 不重置例外
// 会让这条负向用例假通过。
func TestBadSeqRejectedOnMHDeviceType(t *testing.T) {
	e := newEnv(t)
	if code, body := e.post(t, "/registry/environments/local/enterprises/demo/device_types", map[string]any{
		"name": "海思 8W", "short_name": "MH8W",
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("MH 类型应可建，得到 %d %s", code, body)
	}
	e.createDevice(t, "sim_mh")
	if code, body := e.post(t, "/devices/sim_mh/start", map[string]any{
		"environment": "local", "enterprise": "demo", "device_type": "MH8W",
	}); code != http.StatusAccepted {
		t.Fatalf("MH 机型应能挂靠，得到 %d %s", code, body)
	}
	// fault 只能在 Created/Stopped 设；挂靠停机后仍留在 cfg 上。
	if code, body := e.post(t, "/devices/sim_mh/stop", nil); code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/devices/sim_mh/faults", map[string]any{"fault": "bad_seq"}); code != http.StatusBadRequest {
		t.Fatalf("MH 机型上 bad_seq 应 400，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/devices/sim_mh/faults", map[string]any{"fault": "skip_register"}); code != http.StatusOK {
		t.Fatalf("其余 fault 不该被牵连，得到 %d %s", code, body)
	}
	e.createDevice(t, "sim_a3")
	if code, body := e.post(t, "/devices/sim_a3/faults", map[string]any{"fault": "bad_seq"}); code != http.StatusOK {
		t.Fatalf("非 MH 机型 bad_seq 应 200，得到 %d %s", code, body)
	}
}
