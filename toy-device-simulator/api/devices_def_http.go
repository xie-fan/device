package api

import "net/http"

// handleResetConfig 清空临时覆盖，当前值回到产品默认值。Running 下拒绝。
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
	if d.product == "" {
		writeJSON(w, http.StatusOK, configView(d))
		return
	}
	d.overrides = map[string]any{}
	cfg, pruned, err := s.composeDevice(d.product, d.id, d.overrides, d.binding())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d.overrides = pruned
	d.cfg = cfg
	d.playingMode = cfg.PlayingMode
	writeJSON(w, http.StatusOK, configView(d))
}
