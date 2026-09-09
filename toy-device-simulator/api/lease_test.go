package api

import (
	"net/http"
	"testing"
	"time"
)

func leasePath(id string) string { return "/devices/" + id + "/lease" }

func TestLeaseAcquireThenConflict(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_l1")

	code, body := e.post(t, leasePath("sim_l1"), map[string]any{"owner": "runA"})
	if code != http.StatusOK {
		t.Fatalf("首次租应 200，得到 %d %s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "lease_id") == "" || strField(m, "expires_at") == "" {
		t.Fatalf("200 应带 lease_id 与 expires_at: %s", body)
	}
	if _, ok := m["device"].(map[string]any); !ok {
		t.Fatalf("200 应带一份新鲜 device 视图: %s", body)
	}

	code, body = e.post(t, leasePath("sim_l1"), map[string]any{"owner": "runB"})
	if code != http.StatusConflict {
		t.Fatalf("重复租应 409，得到 %d %s", code, body)
	}
	m = decodeMap(t, body)
	if strField(m, "error") != "lease_held" {
		t.Fatalf("409 的 error 应为 lease_held: %s", body)
	}
	if strField(m, "owner") != "runA" || strField(m, "expires_at") == "" {
		t.Fatalf("409 应带 owner 与 expires_at 让人判断该等还是该抢: %s", body)
	}
}

func TestLeaseReleaseNeedsMatchingID(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_l2")
	_, body := e.post(t, leasePath("sim_l2"), map[string]any{"owner": "runA"})
	lid := strField(decodeMap(t, body), "lease_id")

	// 错的 lease_id：200 但没释放，租约仍在。
	code, body := e.del(t, leasePath("sim_l2")+"?lease_id=lse_deadbeef")
	if code != http.StatusOK {
		t.Fatalf("陈旧 lease_id 释放应 200 不报错，得到 %d %s", code, body)
	}
	if decodeMap(t, body)["released"] != false {
		t.Fatalf("错的 lease_id 不该释放: %s", body)
	}
	if code, body := e.post(t, leasePath("sim_l2"), nil); code != http.StatusConflict {
		t.Fatalf("租约应仍在，得到 %d %s", code, body)
	}

	// 对的 lease_id：释放后可再租。
	code, body = e.del(t, leasePath("sim_l2")+"?lease_id="+lid)
	if code != http.StatusOK || decodeMap(t, body)["released"] != true {
		t.Fatalf("对的 lease_id 应释放，得到 %d %s", code, body)
	}
	if code, body := e.post(t, leasePath("sim_l2"), nil); code != http.StatusOK {
		t.Fatalf("释放后应可再租，得到 %d %s", code, body)
	}
}

func TestLeaseExpiresLazily(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_l3")
	if code, body := e.post(t, leasePath("sim_l3"), map[string]any{"ttl_sec": 0.05}); code != http.StatusOK {
		t.Fatalf("租应 200，得到 %d %s", code, body)
	}
	time.Sleep(120 * time.Millisecond)

	// 没有后台清扫协程，靠 acquire 时惰性清账。
	if code, body := e.post(t, leasePath("sim_l3"), nil); code != http.StatusOK {
		t.Fatalf("过期后应可再租，得到 %d %s", code, body)
	}
	if code, body := e.del(t, leasePath("sim_l3")+"?lease_id=x"); code != http.StatusOK {
		t.Fatalf("释放应 200，得到 %d %s", code, body)
	}
}

func TestLeaseStealOverridesHolder(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_l4")
	_, body := e.post(t, leasePath("sim_l4"), map[string]any{"owner": "dead"})
	old := strField(decodeMap(t, body), "lease_id")

	code, body := e.post(t, leasePath("sim_l4"), map[string]any{"owner": "alive", "steal": true})
	if code != http.StatusOK {
		t.Fatalf("steal 应 200，得到 %d %s", code, body)
	}
	fresh := strField(decodeMap(t, body), "lease_id")
	if fresh == "" || fresh == old {
		t.Fatalf("steal 应换一个新 lease_id: old=%s new=%s", old, fresh)
	}
	// 老持有者拿旧 id 来释放，不该把新租约释放掉。
	_, body = e.del(t, leasePath("sim_l4")+"?lease_id="+old)
	if decodeMap(t, body)["released"] != false {
		t.Fatalf("旧 lease_id 不该释放新租约: %s", body)
	}
}

func TestLeaseDiesWithDevice(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_l5")
	e.post(t, leasePath("sim_l5"), map[string]any{"owner": "runA"})
	if code, body := e.del(t, "/devices/sim_l5"); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("删设备应成功，得到 %d %s", code, body)
	}
	e.createDevice(t, "sim_l5")
	if code, body := e.post(t, leasePath("sim_l5"), nil); code != http.StatusOK {
		t.Fatalf("同 id 重建后应可租（租约随对象消失），得到 %d %s", code, body)
	}
}

// 租约是 run 之间的协作锁，不是排他锁——这条测试就是那个决策的文档。
func TestLeaseDoesNotBlockSpeakOrStart(t *testing.T) {
	e := newEnv(t)
	id := "sim_l6"
	assetID := e.uploadWAV(t)
	e.createStartReady(t, id)
	if code, body := e.post(t, leasePath(id), map[string]any{"owner": "runA"}); code != http.StatusOK {
		t.Fatalf("租应 200，得到 %d %s", code, body)
	}

	// 用 /speak 而不是 /speak_and_wait：这里要证的是租约不挡门，不是假连接会不会回话。
	if code, body := e.post(t, "/devices/"+id+"/speak", map[string]any{"asset_id": assetID}); code != http.StatusAccepted {
		t.Fatalf("持租不该挡 speak，得到 %d %s", code, body)
	}
	if code, body := e.post(t, "/devices/"+id+"/stop", nil); code != http.StatusOK {
		t.Fatalf("持租不该挡 stop，得到 %d %s", code, body)
	}
}

func TestLeaseViewFields(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_l7")
	e.post(t, leasePath("sim_l7"), map[string]any{"owner": "runA"})

	code, body, _ := e.get(t, "/devices/sim_l7")
	if code != http.StatusOK {
		t.Fatalf("GET device 应 200，得到 %d %s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "leased_until") == "" || strField(m, "lease_owner") != "runA" {
		t.Fatalf("视图应带 leased_until 与 lease_owner: %s", body)
	}
	if _, leaked := m["lease_id"]; leaked {
		t.Fatalf("lease_id 是释放凭证，不得出现在视图里: %s", body)
	}
	if _, listBody, _ := e.get(t, "/devices"); !containsBytes(listBody, "leased_until") {
		t.Fatalf("列表也该带 leased_until: %s", listBody)
	}
}

func TestLeaseUnknownDevice404(t *testing.T) {
	e := newEnv(t)
	if code, body := e.post(t, leasePath("nope"), nil); code != http.StatusNotFound {
		t.Fatalf("租不存在的设备应 404，得到 %d %s", code, body)
	}
	if code, body := e.del(t, leasePath("nope")+"?lease_id=x"); code != http.StatusNotFound {
		t.Fatalf("释放不存在的设备应 404，得到 %d %s", code, body)
	}
}
