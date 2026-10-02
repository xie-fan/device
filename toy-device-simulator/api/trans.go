package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleTrans 让运行中设备上行一条 '3' 转发消息（对齐基线 trans.go：
// 设备经 WS 调 OpenAPI）。仅 Ready；服务端回包记 trans_response 事件。
func (s *Server) handleTrans(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		EventType string                 `json:"event_type"`
		Path      string                 `json:"path"`
		Header    map[string]interface{} `json:"header"`
		Body      map[string]interface{} `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, "缺 path")
		return
	}
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok || d.inst == nil {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	inst := d.inst
	s.mu.Unlock()
	if err := inst.SendTrans(body.EventType, body.Path, body.Header, body.Body); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.mu.Lock()
	if dd, ok := s.devices[id]; ok {
		dd.lastActivity = time.Now()
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{"sent": true})
}
