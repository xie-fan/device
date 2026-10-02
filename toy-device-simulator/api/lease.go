package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// 租约是 run 之间的协作锁，不是设备的排他锁：UI / /start / /speak / scenario
// 一律不看它。人和 agent 共用一个 manager（phase9.md §3），硬锁会让调试台拿到
// 没有出口的 409；本项目对抢设备的既有姿态是提示 + 显式放行（overridden /
// --dirty），租约照抄这套。真出现 UI 与 agent 互踩，再给变更端点加 lease_id 校验。
//
// 实现约束：两个 handler 全程只碰 s.mu，一次都不调 core。租约与 core 的槽
// （core/slot.go）是不同尺度的两把锁，都是非阻塞 try-acquire，不会互相等待。

const (
	// leaseTTL 一次 run 的上界：wait_ready 30s + speak_and_wait 的 WaitBudget
	// （30s 素材约 70s）≈ 105s，留一倍余量。
	leaseTTL    = 3 * time.Minute
	maxLeaseTTL = 10 * time.Minute
)

// newLeaseID 用 crypto/rand：lease_id 是能力凭证，谁拿到谁能释放，必须不可猜。
func newLeaseID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "lse_" + hex.EncodeToString(b[:])
}

// leaseHeldLocked 惰性过期：过期就地清账，不需要后台清扫——租约的观察者只有
// acquire / deviceView / release 三处，全在 s.mu 里。调用方须持 s.mu。
func leaseHeldLocked(d *managedDevice) bool {
	if d.leaseID == "" {
		return false
	}
	if time.Now().After(d.leaseExpires) {
		d.leaseID, d.leaseOwner, d.leaseExpires = "", "", time.Time{}
		return false
	}
	return true
}

// handlePostLease 获取或续租。带上现任 lease_id 再 POST 一次就是续租：id 不变，
// 只往后推过期时间——音频集逐条送，整组远超 leaseTTL（Phase 13）。
func (s *Server) handlePostLease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Owner   string  `json:"owner"`
		TTLSec  float64 `json:"ttl_sec"`
		Steal   bool    `json:"steal"`
		LeaseID string  `json:"lease_id"` // 续租时给现任 id
	}
	// 空 body 合法：字段都有默认值。
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "JSON 非法")
			return
		}
	}
	ttl := leaseTTL
	if body.TTLSec > 0 {
		ttl = time.Duration(body.TTLSec * float64(time.Second))
		if ttl > maxLeaseTTL {
			ttl = maxLeaseTTL
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	held := leaseHeldLocked(d)
	// 已过期的续租按普通获取处理、发新 id：调用方以响应里的 lease_id 为准。
	renew := held && body.LeaseID != "" && body.LeaseID == d.leaseID
	if held && !renew && !body.Steal {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "lease_held",
			"device_id":  id,
			"owner":      d.leaseOwner,
			"expires_at": d.leaseExpires.UTC().Format(time.RFC3339Nano),
		})
		return
	}
	if !renew {
		d.leaseID = newLeaseID()
	}
	d.leaseOwner = body.Owner
	d.leaseExpires = time.Now().Add(ttl)
	// 回一份新鲜的 deviceView：客户端拿它喂后续的 start/speak，省掉
	// 「GET /devices 到 start 之间设备状态变了」那个窗口。
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":  id,
		"lease_id":   d.leaseID,
		"expires_at": d.leaseExpires.UTC().Format(time.RFC3339Nano),
		"device":     deviceView(d),
	})
}

func (s *Server) handleDeleteLease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lid := r.URL.Query().Get("lease_id")

	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	// 对不上就不动，但仍是 200：manager 重启后拿着陈旧 lease_id 来释放不该报错。
	released := false
	if leaseHeldLocked(d) && lid != "" && d.leaseID == lid {
		d.leaseID, d.leaseOwner, d.leaseExpires = "", "", time.Time{}
		released = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "released": released})
}
