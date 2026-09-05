package api

// 设备定义的读写端点。定义是落盘的基线，agent 按 device_id 引用的就是它；
// PUT /config 改的是本次运行的当前值，不落盘（见 devices_store.go 的 def/cfg 之分）。

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) handleGetDefinition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	def, env, over := d.def, d.defEnv, d.overridden()
	s.mu.Unlock()
	out := configPublic(def, env)
	out["overridden"] = over
	writeJSON(w, http.StatusOK, out)
}

// handlePutDefinition 改定义并落盘。与 PUT /config 的区别：
//   - 不看运行状态——定义改了下次 start 才生效，不影响正在跑的这一轮；
//   - created / stopped 时顺手把当前值也拉齐，否则「改完定义、启动却是旧值」很反直觉。
func (s *Server) handlePutDefinition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rawBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取失败")
		return
	}
	for _, k := range []string{"write_queue_depth", "write_drain_timeout_sec"} {
		if jsonHasKey(rawBytes, k) {
			writeErr(w, http.StatusBadRequest, k)
			return
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if _, ok := raw["device_id"]; ok {
		writeErr(w, http.StatusBadRequest, "device_id 不可变")
		return
	}
	if err := rejectUnknownKeys(raw, putTopAllowed); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rejectExplicitAutoFalse(raw); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	next, newEnv, code, msg := s.patchConfigLocked(d, d.def, d.defEnv, raw)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	d.def, d.defEnv = next, newEnv
	if d.state == stCreated || d.state == stStopped {
		d.resetToDefinitionLocked()
		d.playingMode = next.PlayingMode
	}
	perr := persistWarn("devices.yaml", s.persistDevicesLocked())
	out := configPublic(d.def, d.defEnv)
	out["overridden"] = d.overridden()
	writeJSON(w, http.StatusOK, persistErr(out, perr))
}

// handleResetConfig 丢弃临时修改，当前值回到定义。Running 下拒绝——
// 运行中换采样率/格式会让已发出的帧头与等待预算对不上（与 PUT /config 同一理由）。
func (s *Server) handleResetConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	if d.state == stStarting || d.state == stRunning || d.state == stStopping {
		writeErr(w, http.StatusConflict, "Running 禁止重置配置，请先 stop")
		return
	}
	d.resetToDefinitionLocked()
	d.playingMode = d.cfg.PlayingMode
	out := configPublic(d.cfg, d.envName)
	out["overridden"] = false
	writeJSON(w, http.StatusOK, out)
}
