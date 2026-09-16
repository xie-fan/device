package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
	"toy-device-simulator/manager"
	"toy-device-simulator/recording"
)

func newInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("ins_%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func jsonHasKey(raw []byte, key string) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return valueHasKey(v, key)
}

func valueHasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == key {
				return true
			}
			if valueHasKey(val, key) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if valueHasKey(val, key) {
				return true
			}
		}
	}
	return false
}

func refErrStatus(err error) int {
	if errors.Is(err, manager.ErrRegistryNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// createBody 设备册只收 device_id，或 id_prefix+count。其余字段留下只为认出老调用方。
type createBody struct {
	DeviceID    string          `json:"device_id"`
	Environment string          `json:"environment"`
	Enterprise  string          `json:"enterprise"`
	DeviceType  string          `json:"device_type"`
	Device      json.RawMessage `json:"device"`
	TemplateID  string          `json:"template_id"`
	Count       int             `json:"count"`
	IDPrefix    string          `json:"id_prefix"`
}

func (s *Server) handlePostDevices(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if len(body.Device) > 0 && string(body.Device) != "null" || body.TemplateID != "" ||
		body.Environment != "" || body.Enterprise != "" || body.DeviceType != "" {
		writeErr(w, http.StatusBadRequest, "POST /devices 只收 device_id，或 id_prefix+count")
		return
	}
	var ids []string
	switch {
	case body.DeviceID != "":
		ids = []string{body.DeviceID}
	case body.IDPrefix != "":
		if body.Count <= 0 {
			writeErr(w, http.StatusBadRequest, "count 必须 > 0")
			return
		}
		ids = make([]string, 0, body.Count)
		for i := 1; i <= body.Count; i++ {
			ids = append(ids, body.IDPrefix+"_"+strconv.Itoa(i))
		}
	default:
		writeErr(w, http.StatusBadRequest, "需要 device_id 或 id_prefix+count")
		return
	}
	for _, id := range ids {
		if err := config.ValidatePathComponent(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if _, exists := s.devices[id]; exists {
			writeErr(w, http.StatusConflict, "device_id 冲突")
			return
		}
	}
	deviceIDs := make([]string, 0, len(ids))
	insts := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		d := s.newManaged(id)
		s.devices[id] = d
		deviceIDs = append(deviceIDs, id)
		insts = append(insts, map[string]any{"device_id": id, "instance_id": d.instanceID})
	}
	perr := persistWarn("devices.yaml", s.persistDevicesLocked())
	writeJSON(w, http.StatusCreated, persistErr(map[string]any{"device_ids": deviceIDs, "instances": insts}, perr))
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// newManaged 只按 device_id 建册条目。属性在 start 时从产品合成。
func (s *Server) newManaged(id string) *managedDevice {
	ins := newInstanceID()
	log := core.NewEventLog(id, ins)
	log.SetMaxEntries(s.opts.Config.EventLogMaxEntries)
	evRec := recording.New(false, false, false)
	log.SetMirror(s.eventSink(id, ins, s.opts.RecordingsDir, evRec))
	log.SetOnWSAbort(func() { s.interruptOnWSAbort(id) })
	return &managedDevice{
		id:           id,
		instanceID:   ins,
		cfg:          config.Device{DeviceID: id},
		overrides:    map[string]any{},
		state:        stCreated,
		committed:    map[int]bool{},
		log:          log,
		lastActivity: time.Now(),
		turns:        map[string]*turnRec{},
		evRec:        evRec,
	}
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]map[string]any, 0, len(s.devices))
	for _, d := range s.devices {
		list = append(list, deviceView(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list})
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	writeJSON(w, http.StatusOK, deviceView(d))
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	out := configView(d)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func configPublic(cfg config.Device, envName string) map[string]any {
	ack := cfg.Behavior.DownlinkAck
	autoReg, autoRep := true, true
	if cfg.Behavior.AutoRegister != nil {
		autoReg = *cfg.Behavior.AutoRegister
	}
	if cfg.Behavior.AutoReport != nil {
		autoRep = *cfg.Behavior.AutoReport
	}
	return map[string]any{
		"environment":      envName,
		"enterprise":       cfg.Enterprise,
		"device_type":      cfg.DeviceType,
		"device_id":        cfg.DeviceID,
		"action":           cfg.Action,
		"playing_mode":     cfg.PlayingMode,
		"firmware_version": cfg.FirmwareVersion,
		"nic_type":         cfg.NicType,
		"nic_iccid":        cfg.NicICCID,
		"audio": map[string]any{
			"format": cfg.Audio.Format, "sample_rate": cfg.Audio.SampleRate,
			"channels": cfg.Audio.Channels, "sample_format": cfg.Audio.SampleFormat,
			"slice_ms": cfg.Audio.SliceMs, "max_payload_size": cfg.Audio.MaxPayloadSize,
			"bitrate_kbps": cfg.Audio.BitrateKbps,
		},
		"server": map[string]any{"url": cfg.Server.URL},
		"uuid":   map[string]any{"min": cfg.UUID.Min, "max": cfg.UUID.Max},
		"behavior": map[string]any{
			"auto_register":              autoReg,
			"auto_report":                autoRep,
			"keepalive_interval_sec":     cfg.Behavior.KeepaliveIntervalSec,
			"keepalive_method":           cfg.Behavior.KeepaliveMethod,
			"report_sequence_start":      cfg.Behavior.ReportSequenceStart,
			"report_echo_timeout_sec":    cfg.Behavior.ReportEchoTimeoutSec,
			"register_ack_timeout_sec":   cfg.Behavior.RegisterAckTimeoutSec,
			"first_reply_timeout_sec":    cfg.Behavior.FirstReplyTimeoutSec,
			"downlink_idle_timeout_sec":  cfg.Behavior.DownlinkIdleTimeoutSec,
			"non_audio_followup_sec":     cfg.Behavior.NonAudioFollowupSec,
			"post_final_asr_silence_sec": cfg.Behavior.PostFinalASRSilenceSec,
			"wait_timeout_slack_sec":     cfg.Behavior.WaitTimeoutSlackSec,
			"expect_downlink_need_ack":   cfg.Behavior.ExpectDownlinkNeedAck,
			"speak_backlog_depth":        cfg.Behavior.SpeakBacklogDepth,
			"silence_probe":              cfg.Behavior.SilenceProbe,
			"interrupt_on_disconnect":    cfg.Behavior.InterruptOnDisconnect,
			"downlink_ack": map[string]any{
				"mode": ack.Mode, "sleep_ms": ack.SleepMs, "code": ack.Code,
			},
		},
		"recording": map[string]any{
			"enable_frame_log":    cfg.Recording.EnableFrameLog,
			"save_uplink_audio":   cfg.Recording.SaveUplinkAudio,
			"save_downlink_audio": cfg.Recording.SaveDownlinkAudio,
			"output_dir":          cfg.Recording.OutputDir,
		},
		"features": map[string]any{
			"photo": map[string]any{
				"enabled":              cfg.Features.Photo.Enabled,
				"image":                cfg.Features.Photo.Image,
				"server_default_reply": cfg.Features.Photo.ServerDefaultReply,
				"slice_interval_ms":    cfg.Features.Photo.SliceIntervalMs,
				"reply_timeout_sec":    cfg.Features.Photo.ReplyTimeoutSec,
			},
		},
	}
}

// server 不在 allowlist：url 由环境派生，直设 → 400 未知字段。
var putTopAllowed = map[string]bool{
	"environment": true, "enterprise": true, "device_type": true, "playing_mode": true,
	"audio": true, "uuid": true, "action": true, "firmware_version": true, "firmware": true,
	"nic_type": true, "nic_iccid": true, "downlink_ack": true, "behavior": true, "recording": true,
	"features": true,
}

var putRunningForbidden = map[string]bool{
	"environment": true, "enterprise": true, "device_type": true, "playing_mode": true,
	"audio": true, "uuid": true, "action": true, "firmware_version": true, "firmware": true,
	"nic_type": true, "nic_iccid": true, "downlink_ack": true, "behavior": true,
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rawBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取失败")
		return
	}
	if jsonHasKey(rawBytes, "write_queue_depth") {
		writeErr(w, http.StatusBadRequest, "write_queue_depth")
		return
	}
	if jsonHasKey(rawBytes, "write_drain_timeout_sec") {
		writeErr(w, http.StatusBadRequest, "write_drain_timeout_sec")
		return
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
	if _, ok := raw["product"]; ok {
		writeErr(w, http.StatusBadRequest, "product 请在 start 时给")
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
	if hasAnyKey(raw, "environment", "enterprise", "device_type") {
		writeErr(w, http.StatusBadRequest, "挂靠请在 start 时给，PUT /config 不改 environment/enterprise/device_type")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	if d.product == "" {
		writeErr(w, http.StatusConflict, "设备还没选过产品")
		return
	}
	running := d.state == stStarting || d.state == stRunning || d.state == stStopping
	if running {
		for k := range raw {
			if putRunningForbidden[k] {
				writeErr(w, http.StatusConflict, "Running 禁止改 "+k)
				return
			}
		}
	}
	nextOv := mergeOverrides(d.overrides, flattenJSON("", raw))
	cfg, pruned, err := s.composeDevice(d.product, d.id, nextOv, d.binding())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d.overrides = pruned
	d.cfg = cfg
	if !running {
		d.playingMode = cfg.PlayingMode
	}
	if running && d.inst != nil {
		d.inst.SetFeatures(cfg.Features)
	}
	writeJSON(w, http.StatusOK, configView(d))
}

func hasAnyKey(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func takeStringKey(m map[string]any, key string, dst *string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("%s 类型非法", key)
	}
	*dst = s
	return nil
}

func rejectExplicitAutoFalse(raw map[string]any) error {
	beh, _ := raw["behavior"].(map[string]any)
	if beh == nil {
		return nil
	}
	if err := rejectFalseAutoFlag(beh, "auto_register", "skip_register"); err != nil {
		return err
	}
	return rejectFalseAutoFlag(beh, "auto_report", "skip_report")
}

func rejectFalseAutoFlag(m map[string]any, key, skip string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	flag, ok := v.(bool)
	if !ok {
		return fmt.Errorf("behavior.%s 类型非法", key)
	}
	if !flag {
		return fmt.Errorf("%s 只能为 true，禁止 false（不得映射为 %s）", key, skip)
	}
	return nil
}

func rejectUnknownKeys(m map[string]any, allowed map[string]bool) error {
	for k := range m {
		if !allowed[k] {
			return fmt.Errorf("未知字段 %s", k)
		}
	}
	return nil
}

// applyPutAllowlist 只处理设备级属性；environment/enterprise/device_type
// 是树引用，由 handlePutConfig 先行解析，这里跳过。
func applyPutAllowlist(cfg *config.Device, raw map[string]any) error {
	if v, ok := raw["playing_mode"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("playing_mode 类型非法")
		}
		cfg.PlayingMode = n
	}
	if v, ok := raw["action"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("action 类型非法")
		}
		cfg.Action = s
	}
	if v, ok := raw["firmware_version"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("firmware_version 类型非法")
		}
		cfg.FirmwareVersion = s
	}
	if v, ok := raw["firmware"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("firmware 类型非法")
		}
		cfg.FirmwareVersion = s
	}
	if v, ok := raw["nic_type"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("nic_type 类型非法")
		}
		cfg.NicType = s
	}
	if v, ok := raw["nic_iccid"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("nic_iccid 类型非法")
		}
		cfg.NicICCID = s
	}
	if v, ok := raw["audio"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("audio 类型非法")
		}
		if err := applyPutAudio(&cfg.Audio, m); err != nil {
			return err
		}
	}
	if v, ok := raw["uuid"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("uuid 类型非法")
		}
		if err := applyPutUUID(&cfg.UUID, m); err != nil {
			return err
		}
	}
	if v, ok := raw["downlink_ack"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("downlink_ack 类型非法")
		}
		if err := applyPutAck(&cfg.Behavior.DownlinkAck, m); err != nil {
			return err
		}
	}
	if v, ok := raw["behavior"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("behavior 类型非法")
		}
		if err := applyPutBehavior(&cfg.Behavior, m); err != nil {
			return err
		}
	}
	if v, ok := raw["recording"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("recording 类型非法")
		}
		if err := applyPutRecording(&cfg.Recording, m); err != nil {
			return err
		}
	}
	if v, ok := raw["features"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("features 类型非法")
		}
		if err := applyPutFeatures(&cfg.Features, m); err != nil {
			return err
		}
	}
	return nil
}

func applyPutAudio(a *config.Audio, m map[string]any) error {
	allowed := map[string]bool{
		"format": true, "sample_rate": true, "channels": true,
		"sample_format": true, "slice_ms": true, "max_payload_size": true,
		"bitrate_kbps": true,
	}
	if err := rejectUnknownKeys(m, allowed); err != nil {
		return err
	}
	if v, ok := m["bitrate_kbps"]; ok {
		n, ok := v.(float64)
		if !ok {
			return fmt.Errorf("audio.bitrate_kbps 类型非法")
		}
		a.BitrateKbps = n
	}
	if v, ok := m["format"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("audio.format 类型非法")
		}
		a.Format = s
	}
	if v, ok := m["sample_rate"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("audio.sample_rate 类型非法")
		}
		a.SampleRate = n
	}
	if v, ok := m["channels"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("audio.channels 类型非法")
		}
		a.Channels = n
	}
	if v, ok := m["sample_format"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("audio.sample_format 类型非法")
		}
		a.SampleFormat = s
	}
	if v, ok := m["slice_ms"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("audio.slice_ms 类型非法")
		}
		a.SliceMs = n
	}
	if v, ok := m["max_payload_size"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("audio.max_payload_size 类型非法")
		}
		a.MaxPayloadSize = n
	}
	return nil
}

func applyPutUUID(u *config.UUIDRange, m map[string]any) error {
	if err := rejectUnknownKeys(m, map[string]bool{"min": true, "max": true}); err != nil {
		return err
	}
	if v, ok := m["min"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("uuid.min 类型非法")
		}
		u.Min = n
	}
	if v, ok := m["max"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("uuid.max 类型非法")
		}
		u.Max = n
	}
	return nil
}

func applyPutAck(a *config.DownlinkAck, m map[string]any) error {
	if err := rejectUnknownKeys(m, map[string]bool{"mode": true, "sleep_ms": true, "code": true}); err != nil {
		return err
	}
	if v, ok := m["mode"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("downlink_ack.mode 类型非法")
		}
		a.Mode = s
	}
	if v, ok := m["sleep_ms"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("downlink_ack.sleep_ms 类型非法")
		}
		a.SleepMs = n
	}
	if v, ok := m["code"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("downlink_ack.code 类型非法")
		}
		a.Code = n
	}
	return nil
}

func applyPutBehavior(b *config.Behavior, m map[string]any) error {
	allowed := map[string]bool{
		"auto_register": true, "auto_report": true,
		"keepalive_interval_sec": true, "keepalive_method": true,
		"report_sequence_start": true, "report_echo_timeout_sec": true,
		"register_ack_timeout_sec": true, "first_reply_timeout_sec": true,
		"downlink_idle_timeout_sec": true, "non_audio_followup_sec": true,
		"post_final_asr_silence_sec": true, "wait_timeout_slack_sec": true,
		"expect_downlink_need_ack": true, "downlink_ack": true,
		"speak_backlog_depth": true, "silence_probe": true,
		"interrupt_on_disconnect": true,
	}
	if err := rejectUnknownKeys(m, allowed); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "speak_backlog_depth", &b.SpeakBacklogDepth); err != nil {
		return err
	}
	if v, ok := m["auto_register"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("behavior.auto_register 类型非法")
		}
		if !flag {
			return fmt.Errorf("auto_register 只能为 true，禁止 false（不得映射为 skip_register）")
		}
		b.AutoRegister = &flag
	}
	if v, ok := m["auto_report"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("behavior.auto_report 类型非法")
		}
		if !flag {
			return fmt.Errorf("auto_report 只能为 true，禁止 false（不得映射为 skip_report）")
		}
		b.AutoReport = &flag
	}
	if err := setBehaviorInt(m, "keepalive_interval_sec", &b.KeepaliveIntervalSec); err != nil {
		return err
	}
	if v, ok := m["keepalive_method"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("behavior.keepalive_method 类型非法")
		}
		b.KeepaliveMethod = s
	}
	if err := setBehaviorInt(m, "report_sequence_start", &b.ReportSequenceStart); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "report_echo_timeout_sec", &b.ReportEchoTimeoutSec); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "register_ack_timeout_sec", &b.RegisterAckTimeoutSec); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "first_reply_timeout_sec", &b.FirstReplyTimeoutSec); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "downlink_idle_timeout_sec", &b.DownlinkIdleTimeoutSec); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "non_audio_followup_sec", &b.NonAudioFollowupSec); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "post_final_asr_silence_sec", &b.PostFinalASRSilenceSec); err != nil {
		return err
	}
	if err := setBehaviorInt(m, "wait_timeout_slack_sec", &b.WaitTimeoutSlackSec); err != nil {
		return err
	}
	if v, ok := m["expect_downlink_need_ack"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("behavior.expect_downlink_need_ack 类型非法")
		}
		b.ExpectDownlinkNeedAck = flag
	}
	if v, ok := m["silence_probe"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("behavior.silence_probe 类型非法")
		}
		b.SilenceProbe = flag
	}
	if v, ok := m["interrupt_on_disconnect"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("behavior.interrupt_on_disconnect 类型非法")
		}
		b.InterruptOnDisconnect = flag
	}
	if v, ok := m["downlink_ack"]; ok {
		ack, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("behavior.downlink_ack 类型非法")
		}
		if err := applyPutAck(&b.DownlinkAck, ack); err != nil {
			return err
		}
	}
	return nil
}

func setBehaviorInt(m map[string]any, key string, dst *int) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	n, ok := jsonToInt(v)
	if !ok {
		return fmt.Errorf("behavior.%s 类型非法", key)
	}
	*dst = n
	return nil
}

func applyPutRecording(rec *config.Recording, m map[string]any) error {
	allowed := map[string]bool{
		"enable_frame_log": true, "save_uplink_audio": true,
		"save_downlink_audio": true, "output_dir": true,
	}
	if err := rejectUnknownKeys(m, allowed); err != nil {
		return err
	}
	if v, ok := m["enable_frame_log"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("recording.enable_frame_log 类型非法")
		}
		rec.EnableFrameLog = flag
	}
	if v, ok := m["save_uplink_audio"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("recording.save_uplink_audio 类型非法")
		}
		rec.SaveUplinkAudio = flag
	}
	if v, ok := m["save_downlink_audio"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("recording.save_downlink_audio 类型非法")
		}
		rec.SaveDownlinkAudio = flag
	}
	if v, ok := m["output_dir"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("recording.output_dir 类型非法")
		}
		rec.OutputDir = s
	}
	return nil
}

func applyPutFeatures(f *config.Features, m map[string]any) error {
	if err := rejectUnknownKeys(m, map[string]bool{"photo": true}); err != nil {
		return err
	}
	v, ok := m["photo"]
	if !ok {
		return nil
	}
	pm, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("features.photo 类型非法")
	}
	return applyPutPhoto(&f.Photo, pm)
}

func applyPutPhoto(p *config.PhotoFeature, m map[string]any) error {
	allowed := map[string]bool{
		"enabled": true, "image": true, "server_default_reply": true,
		"slice_interval_ms": true, "reply_timeout_sec": true,
	}
	if err := rejectUnknownKeys(m, allowed); err != nil {
		return err
	}
	if v, ok := m["enabled"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("features.photo.enabled 类型非法")
		}
		p.Enabled = flag
	}
	if v, ok := m["image"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("features.photo.image 类型非法")
		}
		p.Image = s
	}
	if v, ok := m["server_default_reply"]; ok {
		flag, ok := v.(bool)
		if !ok {
			return fmt.Errorf("features.photo.server_default_reply 类型非法")
		}
		p.ServerDefaultReply = flag
	}
	if v, ok := m["slice_interval_ms"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("features.photo.slice_interval_ms 类型非法")
		}
		p.SliceIntervalMs = n
	}
	if v, ok := m["reply_timeout_sec"]; ok {
		n, ok := jsonToInt(v)
		if !ok {
			return fmt.Errorf("features.photo.reply_timeout_sec 类型非法")
		}
		p.ReplyTimeoutSec = n
	}
	return nil
}

func jsonToInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

func (s *Server) handleFaults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Fault string `json:"fault"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	f, err := core.ParseFault(body.Fault)
	if err != nil {
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
	if d.state != stCreated && d.state != stStopped {
		writeErr(w, http.StatusConflict, "仅 Created/Stopped 可设 fault")
		return
	}
	// 服务端对 MH 机型有 Seq 不重置例外：错误序号不会被丢，bad_seq 会假通过。
	// 宁可这里 400，也不给一个骗人的绿。
	if f == core.FaultBadSeq && config.IsSeqExemptDeviceType(d.cfg.DeviceType) {
		writeErr(w, http.StatusBadRequest,
			"device_type "+d.cfg.DeviceType+" 命中服务端 Seq 不重置例外，bad_seq 用例会假通过；换一个非 MH 机型跑")
		return
	}
	d.fault = f
	writeJSON(w, http.StatusOK, map[string]any{"fault": body.Fault})
}

func (s *Server) acquireConnLocked() bool {
	max := s.opts.Config.MaxConnections
	if s.connUsed >= max {
		return false
	}
	s.connUsed++
	return true
}

func (s *Server) releaseConnLocked() {
	if s.connUsed > 0 {
		s.connUsed--
	}
}

func (s *Server) tryAcquireSpeak() bool {
	max := int64(s.opts.Config.MaxConcurrentSpeaking)
	for {
		cur := s.speakUsed.Load()
		if cur >= max {
			return false
		}
		if s.speakUsed.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (s *Server) releaseSpeak() {
	for {
		cur := s.speakUsed.Load()
		if cur <= 0 {
			return
		}
		if s.speakUsed.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

func (s *Server) touchActivity(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.devices[id]; ok {
		d.lastActivity = time.Now()
	}
}

func (s *Server) onTurnTerminal(deviceID, turnID string, ev core.Event) {
	s.mu.Lock()
	if d, ok := s.devices[deviceID]; ok {
		tr := d.turns[turnID]
		if tr == nil {
			tr = &turnRec{TurnID: turnID, InstanceID: d.instanceID}
			d.turns[turnID] = tr
		}
		tr.EndReason = ev.EndReason
		tr.UplinkReason = ev.UplinkReason
		tr.ReplyKind = ev.ReplyKind
		tr.DownFormat = ev.DownFormat
		tr.DownBytes = ev.DownBytes
		if ev.UpFormat != "" {
			tr.UpFormat = ev.UpFormat
		}
		d.lastActivity = time.Now()
	}
	s.mu.Unlock()
	s.releaseSpeak()
}

// onTurnStarted：speak backlog 出队真正启动时补 turnRec 的 uuid/seq_before。
func (s *Server) onTurnStarted(deviceID, turnID string, uuid uint32, seqBefore int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	if !ok {
		return
	}
	tr := d.turns[turnID]
	if tr == nil {
		tr = &turnRec{TurnID: turnID, InstanceID: d.instanceID}
		d.turns[turnID] = tr
	}
	tr.UplinkUUID = uuid
	tr.SeqBefore = seqBefore
	d.lastActivity = time.Now()
}
