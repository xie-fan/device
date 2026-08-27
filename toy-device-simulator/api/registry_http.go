package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"toy-device-simulator/manager"
)

// 配置树端点。键（环境名、两级简称）创建后不可改：改键 = 删掉重建。
// 仅设备类型的删除需要查 live 设备引用：设备必引用完整三级路径，
// 厂商删除被「下有类型」挡住、环境删除被「下有厂商」挡住，引用不会悬空。

func regErrStatus(err error) int {
	switch {
	case errors.Is(err, manager.ErrRegistryNotFound):
		return http.StatusNotFound
	case errors.Is(err, manager.ErrRegistryConflict):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) handleGetRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"environments": s.reg.Snapshot()})
}

func (s *Server) handlePostEnvironment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := s.reg.AddEnvironment(body.Name, body.URL); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": body.Name, "url": body.URL})
}

func (s *Server) handlePutEnvironment(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := s.reg.UpdateEnvironmentURL(env, body.URL); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": env, "url": body.URL})
}

func (s *Server) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := s.reg.DeleteEnvironment(r.PathValue("env")); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePostEnterprise(w http.ResponseWriter, r *http.Request) {
	env := r.PathValue("env")
	var body struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := s.reg.AddEnterprise(env, body.Name, body.ShortName); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": body.Name, "short_name": body.ShortName})
}

func (s *Server) handlePutEnterprise(w http.ResponseWriter, r *http.Request) {
	env, short := r.PathValue("env"), r.PathValue("short")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := s.reg.UpdateEnterpriseName(env, short, body.Name); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": body.Name, "short_name": short})
}

func (s *Server) handleDeleteEnterprise(w http.ResponseWriter, r *http.Request) {
	if err := s.reg.DeleteEnterprise(r.PathValue("env"), r.PathValue("short")); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePostDeviceType(w http.ResponseWriter, r *http.Request) {
	env, short := r.PathValue("env"), r.PathValue("short")
	var body struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := s.reg.AddDeviceType(env, short, body.Name, body.ShortName); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": body.Name, "short_name": body.ShortName})
}

func (s *Server) handlePutDeviceType(w http.ResponseWriter, r *http.Request) {
	env, short, tshort := r.PathValue("env"), r.PathValue("short"), r.PathValue("tshort")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := s.reg.UpdateDeviceTypeName(env, short, tshort, body.Name); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": body.Name, "short_name": tshort})
}

func (s *Server) handleDeleteDeviceType(w http.ResponseWriter, r *http.Request) {
	env, short, tshort := r.PathValue("env"), r.PathValue("short"), r.PathValue("tshort")
	// 与设备创建/挂靠的 Resolve 同在 s.mu 临界区，检查-删除不与建表交错。
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.envName == env && d.cfg.Enterprise == short && d.cfg.DeviceType == tshort {
			writeErr(w, http.StatusConflict, "类型仍被设备引用: "+d.id)
			return
		}
	}
	if err := s.reg.DeleteDeviceType(env, short, tshort); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
