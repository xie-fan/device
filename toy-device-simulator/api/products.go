package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"toy-device-simulator/config"
)

var productDefaultsAllowed = map[string]bool{
	"playing_mode": true, "audio": true, "uuid": true, "action": true,
	"firmware_version": true, "firmware": true, "nic_type": true, "nic_iccid": true,
	"downlink_ack": true, "behavior": true, "recording": true, "features": true,
}

type productBody struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	PlayingModes []int           `json:"playing_modes"`
	AudioFormats []string        `json:"audio_formats"`
	Defaults     json.RawMessage `json:"defaults"`
}

func productJSON(p config.Product) map[string]any {
	def := configPublic(p.Defaults, "")
	delete(def, "environment")
	delete(def, "enterprise")
	delete(def, "device_type")
	delete(def, "device_id")
	delete(def, "server")
	return map[string]any{
		"id":            p.ID,
		"name":          p.Name,
		"playing_modes": p.PlayingModes,
		"audio_formats": p.AudioFormats,
		"defaults":      def,
	}
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	list := s.products.List()
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, productJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": out})
}

func (s *Server) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := s.products.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "产品不存在")
		return
	}
	writeJSON(w, http.StatusOK, productJSON(p))
}

func (s *Server) handlePostProduct(w http.ResponseWriter, r *http.Request) {
	var body productBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	p, err := parseProduct(body, "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.products.Add(p); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, productJSON(p))
}

func (s *Server) handlePutProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body productBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	p, err := parseProduct(body, id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.products.Update(id, p); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	got, _ := s.products.Get(id)
	writeJSON(w, http.StatusOK, productJSON(got))
}

func (s *Server) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if refs := s.reg.ProductReferences(id); len(refs) > 0 {
		writeErr(w, http.StatusConflict, "产品正被设备类型设为默认产品: "+strings.Join(refs, ", "))
		return
	}
	for _, d := range s.devices {
		d.syncRunning()
		if d.product != id {
			continue
		}
		if d.state == stStarting || d.state == stRunning {
			writeErr(w, http.StatusConflict, "产品正被运行中的设备使用: "+d.id)
			return
		}
	}
	if err := s.products.Delete(id); err != nil {
		writeErr(w, regErrStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseProduct(body productBody, pathID string) (config.Product, error) {
	if pathID != "" && body.ID != "" && body.ID != pathID {
		return config.Product{}, fmt.Errorf("id 与路径不符")
	}
	id := body.ID
	if id == "" {
		id = pathID
	}
	p := config.Product{
		ID:           id,
		Name:         body.Name,
		PlayingModes: body.PlayingModes,
		AudioFormats: body.AudioFormats,
		Defaults:     config.DefaultProduct().Defaults,
	}
	if len(body.Defaults) > 0 && string(body.Defaults) != "null" {
		if jsonHasKey(body.Defaults, "write_queue_depth") || jsonHasKey(body.Defaults, "write_drain_timeout_sec") {
			return config.Product{}, fmt.Errorf("write_queue")
		}
		for _, k := range []string{"device_id", "enterprise", "device_type", "server"} {
			if jsonHasKey(body.Defaults, k) {
				return config.Product{}, fmt.Errorf("defaults 不得含 %s", k)
			}
		}
		var raw map[string]any
		if err := json.Unmarshal(body.Defaults, &raw); err != nil {
			return config.Product{}, err
		}
		if err := rejectUnknownKeys(raw, productDefaultsAllowed); err != nil {
			return config.Product{}, err
		}
		if err := rejectExplicitAutoFalse(raw); err != nil {
			return config.Product{}, err
		}
		if err := applyPutAllowlist(&p.Defaults, raw); err != nil {
			return config.Product{}, err
		}
	}
	if err := config.ValidateProduct(p); err != nil {
		return config.Product{}, err
	}
	return p, nil
}

type bindingRef struct {
	env, enterprise, deviceType, serverURL string
}

func (d *managedDevice) binding() bindingRef {
	return bindingRef{d.envName, d.cfg.Enterprise, d.cfg.DeviceType, d.cfg.Server.URL}
}

func (d *managedDevice) overridden() bool {
	return len(d.overrides) > 0
}

func overrideObj(d *managedDevice) map[string]any {
	if len(d.overrides) == 0 {
		return map[string]any{}
	}
	return cloneMap(d.overrides)
}

func configView(d *managedDevice) map[string]any {
	out := configPublic(d.cfg, d.envName)
	out["product"] = d.product
	out["overrides"] = overrideObj(d)
	out["overridden"] = d.overridden()
	return out
}

func flattenJSON(prefix string, m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			for sk, sv := range flattenJSON(key, sub) {
				out[sk] = sv
			}
			continue
		}
		out[key] = v
	}
	return out
}

func unflatten(ov map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for path, v := range ov {
		if err := assignPath(out, strings.Split(path, "."), v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func assignPath(m map[string]any, parts []string, v any) error {
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Errorf("覆盖路径非法")
	}
	if len(parts) == 1 {
		m[parts[0]] = v
		return nil
	}
	cur, exists := m[parts[0]]
	if exists {
		sub, ok := cur.(map[string]any)
		if !ok {
			return fmt.Errorf("覆盖路径冲突 %s", parts[0])
		}
		return assignPath(sub, parts[1:], v)
	}
	sub := map[string]any{}
	m[parts[0]] = sub
	return assignPath(sub, parts[1:], v)
}

func overridePathForbidden(path string) bool {
	switch path {
	case "device_id", "environment", "enterprise", "device_type", "product",
		"behavior.write_queue_depth", "behavior.write_drain_timeout_sec":
		return true
	}
	return path == "server" || strings.HasPrefix(path, "server.")
}

func isJSONScalar(v any) bool {
	switch v.(type) {
	case nil, bool, string, float64, json.Number, int, int64:
		return true
	default:
		return false
	}
}

func checkOverrides(ov map[string]any) error {
	for k, v := range ov {
		if !isJSONScalar(v) {
			return fmt.Errorf("覆盖 %s 必须是标量", k)
		}
		if overridePathForbidden(k) {
			return fmt.Errorf("覆盖禁止 %s", k)
		}
	}
	nested, err := unflatten(ov)
	if err != nil {
		return err
	}
	if err := rejectUnknownKeys(nested, putTopAllowed); err != nil {
		return err
	}
	if hasAnyKey(nested, "environment", "enterprise", "device_type") {
		return fmt.Errorf("覆盖禁止挂靠字段")
	}
	var dummy config.Device
	return applyPutAllowlist(&dummy, nested)
}

func publicKey(path string) string {
	if path == "firmware" {
		return "firmware_version"
	}
	if path == "downlink_ack" || strings.HasPrefix(path, "downlink_ack.") {
		return "behavior." + path
	}
	return path
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func sameScalar(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af == bf
	}
	return a == b
}

func pruneOverrides(defaults, current config.Device, ov map[string]any) map[string]any {
	pubDef := flattenJSON("", configPublic(defaults, ""))
	pubCur := flattenJSON("", configPublic(current, ""))
	pruned := map[string]any{}
	for k, v := range ov {
		pk := publicKey(k)
		if sameScalar(pubCur[pk], pubDef[pk]) {
			continue
		}
		pruned[k] = v
	}
	return pruned
}

func (s *Server) composeDevice(productID, deviceID string, overrides map[string]any, b bindingRef) (config.Device, map[string]any, error) {
	if err := checkOverrides(overrides); err != nil {
		return config.Device{}, nil, err
	}
	prod, ok := s.products.Get(productID)
	if !ok {
		return config.Device{}, nil, fmt.Errorf("产品不存在")
	}
	cfg := prod.Defaults
	if len(overrides) > 0 {
		nested, err := unflatten(overrides)
		if err != nil {
			return config.Device{}, nil, err
		}
		if err := applyPutAllowlist(&cfg, nested); err != nil {
			return config.Device{}, nil, err
		}
	}
	pruned := pruneOverrides(prod.Defaults, cfg, overrides)
	cfg.DeviceID = deviceID
	cfg.Behavior.WriteQueueDepth = s.opts.Config.WriteQueueDepth
	cfg.Behavior.WriteDrainTimeoutSec = s.opts.Config.WriteDrainTimeoutSec
	if cfg.Recording.OutputDir == "" {
		cfg.Recording.OutputDir = s.opts.RecordingsDir
	}
	cfg = bindDevice(cfg, b.env, b.enterprise, b.deviceType, b.serverURL)
	if err := config.ValidatePhase2(cfg); err != nil {
		return config.Device{}, nil, err
	}
	return cfg, pruned, nil
}

func mergeOverrides(base, extra map[string]any) map[string]any {
	out := cloneMap(base)
	for k, v := range extra {
		out[k] = v
	}
	return out
}
