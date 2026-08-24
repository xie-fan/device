package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
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

func (s *Server) parseDevice(raw json.RawMessage) (config.Device, error) {
	if jsonHasKey(raw, "write_queue_depth") || jsonHasKey(raw, "write_drain_timeout_sec") {
		return config.Device{}, fmt.Errorf("write_queue")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return config.Device{}, err
	}
	beh, _ := m["behavior"].(map[string]any)
	if beh == nil {
		beh = map[string]any{}
		m["behavior"] = beh
	}
	beh["write_queue_depth"] = s.opts.Config.WriteQueueDepth
	beh["write_drain_timeout_sec"] = s.opts.Config.WriteDrainTimeoutSec
	wrapped, err := yaml.Marshal(map[string]any{"device": m})
	if err != nil {
		return config.Device{}, err
	}
	cfg, err := config.LoadPhase2(wrapped)
	if err != nil {
		return config.Device{}, err
	}
	if cfg.Recording.OutputDir == "" {
		cfg.Recording.OutputDir = s.opts.RecordingsDir
	}
	return cfg, nil
}

type createBody struct {
	Device     json.RawMessage `json:"device"`
	TemplateID string          `json:"template_id"`
	Count      int             `json:"count"`
	IDPrefix   string          `json:"id_prefix"`
}

func (s *Server) handlePostDevices(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if len(body.Device) > 0 && string(body.Device) != "null" {
		cfg, err := s.parseDevice(body.Device)
		if err != nil {
			if strings.Contains(err.Error(), "write_queue") {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		if _, exists := s.devices[cfg.DeviceID]; exists {
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "device_id 冲突")
			return
		}
		d := s.newManaged(cfg)
		s.devices[cfg.DeviceID] = d
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{
			"device_ids": []string{cfg.DeviceID},
			"instances": []map[string]any{{
				"device_id": cfg.DeviceID, "instance_id": d.instanceID,
			}},
		})
		return
	}
	if body.TemplateID == "" || body.Count <= 0 {
		writeErr(w, http.StatusBadRequest, "需要 device 或 template_id+count")
		return
	}
	if err := config.ValidatePathComponent(body.TemplateID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tmpl, err := s.readTemplate(body.TemplateID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "模板不存在")
		return
	}
	ids := make([]string, 0, body.Count)
	for i := 1; i <= body.Count; i++ {
		ids = append(ids, body.IDPrefix+"_"+strconv.Itoa(i))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if _, exists := s.devices[id]; exists {
			writeErr(w, http.StatusConflict, "批量 ID 冲突")
			return
		}
	}
	type created struct {
		id  string
		dev *managedDevice
	}
	var made []created
	for _, id := range ids {
		raw := cloneMap(tmpl)
		raw["device_id"] = id
		b, _ := json.Marshal(raw)
		cfg, err := s.parseDevice(b)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		d := s.newManaged(cfg)
		s.devices[id] = d
		made = append(made, created{id, d})
	}
	deviceIDs := make([]string, 0, len(made))
	insts := make([]map[string]any, 0, len(made))
	for _, c := range made {
		deviceIDs = append(deviceIDs, c.id)
		insts = append(insts, map[string]any{"device_id": c.id, "instance_id": c.dev.instanceID})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device_ids": deviceIDs, "instances": insts})
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Server) newManaged(cfg config.Device) *managedDevice {
	ins := newInstanceID()
	log := core.NewEventLog(cfg.DeviceID, ins)
	log.SetMaxEntries(s.opts.Config.EventLogMaxEntries)
	return &managedDevice{
		id:           cfg.DeviceID,
		instanceID:   ins,
		cfg:          cfg,
		state:        stCreated,
		committed:    map[int]bool{},
		log:          log,
		lastActivity: time.Now(),
		turns:        map[string]*turnRec{},
		playingMode:  cfg.PlayingMode,
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
	cfg := d.cfg
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, configPublic(cfg))
}

func configPublic(cfg config.Device) map[string]any {
	ack := cfg.Behavior.DownlinkAck
	autoReg, autoRep := true, true
	if cfg.Behavior.AutoRegister != nil {
		autoReg = *cfg.Behavior.AutoRegister
	}
	if cfg.Behavior.AutoReport != nil {
		autoRep = *cfg.Behavior.AutoReport
	}
	return map[string]any{
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
	}
}

var putTopAllowed = map[string]bool{
	"enterprise": true, "device_type": true, "playing_mode": true, "audio": true,
	"server": true, "uuid": true, "action": true, "firmware_version": true, "firmware": true,
	"nic_type": true, "nic_iccid": true, "downlink_ack": true, "behavior": true, "recording": true,
}

var putRunningForbidden = map[string]bool{
	"enterprise": true, "device_type": true, "playing_mode": true, "audio": true,
	"server": true, "uuid": true, "action": true, "firmware_version": true, "firmware": true,
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
	if err := rejectUnknownKeys(raw, putTopAllowed); err != nil {
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
	if err := rejectExplicitAutoFalse(raw); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	next := d.cfg
	if err := applyPutAllowlist(&next, raw); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.ValidatePhase2(next); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d.cfg = next
	if !running {
		d.playingMode = next.PlayingMode
	}
	writeJSON(w, http.StatusOK, configPublic(d.cfg))
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

func applyPutAllowlist(cfg *config.Device, raw map[string]any) error {
	if v, ok := raw["enterprise"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("enterprise 类型非法")
		}
		cfg.Enterprise = s
	}
	if v, ok := raw["device_type"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("device_type 类型非法")
		}
		cfg.DeviceType = s
	}
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
	if v, ok := raw["server"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("server 类型非法")
		}
		if err := applyPutServer(&cfg.Server, m); err != nil {
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
	return nil
}

func applyPutAudio(a *config.Audio, m map[string]any) error {
	allowed := map[string]bool{
		"format": true, "sample_rate": true, "channels": true,
		"sample_format": true, "slice_ms": true, "max_payload_size": true,
	}
	if err := rejectUnknownKeys(m, allowed); err != nil {
		return err
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

func applyPutServer(s *config.Server, m map[string]any) error {
	if err := rejectUnknownKeys(m, map[string]bool{"url": true}); err != nil {
		return err
	}
	if v, ok := m["url"]; ok {
		u, ok := v.(string)
		if !ok {
			return fmt.Errorf("server.url 类型非法")
		}
		s.URL = u
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
	}
	if err := rejectUnknownKeys(m, allowed); err != nil {
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
	d.fault = f
	writeJSON(w, http.StatusOK, map[string]any{"fault": body.Fault})
}

func (s *Server) handlePostTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID string          `json:"template_id"`
		Device     json.RawMessage `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if body.TemplateID == "" {
		writeErr(w, http.StatusBadRequest, "缺 template_id")
		return
	}
	if jsonHasKey(body.Device, "device_id") {
		writeErr(w, http.StatusBadRequest, "模板禁止 device_id")
		return
	}
	if jsonHasKey(body.Device, "write_queue_depth") || jsonHasKey(body.Device, "write_drain_timeout_sec") {
		writeErr(w, http.StatusBadRequest, "模板禁止 write_queue_*")
		return
	}
	if err := config.ValidatePathComponent(body.TemplateID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir := s.templatesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var dev map[string]any
	if err := json.Unmarshal(body.Device, &dev); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := yaml.Marshal(map[string]any{"device": dev})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := filepath.Join(dir, body.TemplateID+".yaml")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"template_id": body.TemplateID})
}

func (s *Server) templatesDir() string {
	if s.opts.TemplatesDir != "" {
		return s.opts.TemplatesDir
	}
	return filepath.Join("configs", "templates")
}

func (s *Server) readTemplate(id string) (map[string]any, error) {
	if err := config.ValidatePathComponent(id); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(s.templatesDir(), id+".yaml"))
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Device map[string]any `yaml:"device"`
	}
	if err := yaml.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	return wrap.Device, nil
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	dir := s.templatesDir()
	ents, _ := os.ReadDir(dir)
	ids := []string{}
	for _, e := range ents {
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") {
			ids = append(ids, strings.TrimSuffix(name, ".yaml"))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": ids})
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := config.ValidatePathComponent(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := s.readTemplate(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "模板不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template_id": id, "device": m})
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := config.ValidatePathComponent(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = os.Remove(filepath.Join(s.templatesDir(), id+".yaml"))
	w.WriteHeader(http.StatusNoContent)
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
		d.lastActivity = time.Now()
	}
	s.mu.Unlock()
	s.releaseSpeak()
}
