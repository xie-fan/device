package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"toy-device-simulator/core"
)

type speakBody struct {
	AssetID    string        `json:"asset_id"`
	Stream     []streamEntry `json:"stream"`
	TimeoutSec *float64      `json:"timeout_sec"`
}

type streamEntry struct {
	Type       string `json:"type"`
	AssetID    string `json:"asset_id"`
	DurationMs int    `json:"duration_ms"`
}

func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	s.doSpeak(w, r, false)
}

func (s *Server) handleSpeakAndWait(w http.ResponseWriter, r *http.Request) {
	s.doSpeak(w, r, true)
}

func (s *Server) doSpeak(w http.ResponseWriter, r *http.Request, wait bool) {
	id := r.PathValue("id")
	var body speakBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	hasAsset := body.AssetID != ""
	hasStream := len(body.Stream) > 0
	if hasAsset == hasStream {
		writeErr(w, http.StatusBadRequest, "asset_id 与 stream 互斥且必须择一")
		return
	}
	if hasStream {
		maxN := s.opts.Config.MaxStreamEntries
		if maxN > 0 && len(body.Stream) > maxN {
			writeErr(w, http.StatusBadRequest, "stream.length 超过 max_stream_entries")
			return
		}
	}

	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	d.syncRunning()
	snapGen := d.gen
	snapInst := d.inst
	snapIns := d.instanceID
	sr, ch, sf := d.cfg.Audio.SampleRate, d.cfg.Audio.Channels, d.cfg.Audio.SampleFormat
	s.mu.Unlock()

	pcm, durMs, errCode, errMsg := s.buildSpeakPCM(body, sr, ch, sf)
	if errCode != 0 {
		writeErr(w, errCode, errMsg)
		return
	}
	if hasStream {
		maxDur := s.opts.Config.MaxStreamDurationSec
		if maxDur > 0 && durMs > maxDur*1000 {
			writeErr(w, http.StatusBadRequest, "stream 总时长超 max_stream_duration_sec")
			return
		}
	}

	s.mu.Lock()
	d, ok = s.devices[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	d.syncRunning()
	if d.cfg.Audio.SampleRate != sr || d.cfg.Audio.Channels != ch || d.cfg.Audio.SampleFormat != sf {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "audio_config_changed")
		return
	}
	if snapInst != d.inst || d.instanceID != snapIns || (snapGen != 0 && d.gen != snapGen) {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "generation_changed")
		return
	}
	if d.state != stRunning || d.inst == nil || !core.Speakable(d.inst.ConnectionState(), d.fault) {
		st, cs := d.state, d.connState()
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "not_speakable", "instance_state": st, "connection_state": cs,
		})
		return
	}
	if d.inst.FinalizeStarted() {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "finalize_started")
		return
	}
	inst := d.inst
	ins := d.instanceID
	s.mu.Unlock()

	turnID, uuid, seqBefore, err := inst.SpeakPermit(pcm, s.tryAcquireSpeak)
	if err != nil {
		if errors.Is(err, core.ErrSpeakPermit) {
			writeErr(w, http.StatusTooManyRequests, "speak_permit")
			return
		}
		writeErr(w, http.StatusConflict, err.Error())
		return
	}

	s.mu.Lock()
	if dd, ok := s.devices[id]; ok {
		tr := dd.turns[turnID]
		if tr == nil {
			tr = &turnRec{TurnID: turnID}
			dd.turns[turnID] = tr
		}
		tr.InstanceID = ins
		tr.UplinkUUID = uuid
		tr.SeqBefore = seqBefore
		tr.SampleRate = sr
		tr.Channels = ch
		tr.OutputDir = inst.Config().Recording.OutputDir
		dd.lastActivity = time.Now()
	}
	s.mu.Unlock()

	resp := map[string]any{
		"turn_id": turnID, "uplink_uuid": uuid, "seq_before": seqBefore, "instance_id": ins,
	}
	if !wait {
		writeJSON(w, http.StatusAccepted, resp)
		return
	}
	timeout := inst.WaitBudgetFor(len(pcm))
	if body.TimeoutSec != nil {
		timeout = time.Duration(*body.TimeoutSec * float64(time.Second))
	}
	ev, err := inst.WaitTurn(turnID, timeout)
	if err != nil {
		if errors.Is(err, core.ErrWaitTimeout) {
			writeErr(w, http.StatusGatewayTimeout, "speak_and_wait 超时")
			return
		}
		writeErr(w, http.StatusGatewayTimeout, err.Error())
		return
	}
	s.mu.Lock()
	if dd, ok := s.devices[id]; ok {
		if tr, ok := dd.turns[turnID]; ok {
			tr.EndReason = ev.EndReason
			tr.UplinkReason = ev.UplinkReason
			tr.ReplyKind = ev.ReplyKind
		}
	}
	s.mu.Unlock()
	resp["turn_end_reason"] = ev.EndReason
	resp["uplink_end_reason"] = ev.UplinkReason
	resp["reply_kind"] = ev.ReplyKind
	resp["event_seq"] = ev.EventSeq
	resp["event_type"] = ev.Type
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) buildSpeakPCM(body speakBody, sr, ch int, sf string) (pcm []byte, durMs, code int, msg string) {
	if body.AssetID != "" {
		raw, _, err := s.copyAsset(body.AssetID)
		if err != nil {
			return nil, 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
		}
		p, err := core.DecodeWAV(raw)
		if err != nil {
			return nil, 0, http.StatusBadRequest, "非 WAV"
		}
		if err := p.Match(sr, ch, sf); err != nil {
			return nil, 0, http.StatusBadRequest, "WAV fmt 与设备 audio_* 不符"
		}
		return p.Samples, wavDurationMs(p), 0, ""
	}
	var out []byte
	total := 0
	for _, e := range body.Stream {
		switch e.Type {
		case "audio":
			raw, _, err := s.copyAsset(e.AssetID)
			if err != nil {
				return nil, 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
			}
			p, err := core.DecodeWAV(raw)
			if err != nil {
				return nil, 0, http.StatusBadRequest, "非 WAV"
			}
			if err := p.Match(sr, ch, sf); err != nil {
				return nil, 0, http.StatusBadRequest, "WAV fmt 与设备 audio_* 不符"
			}
			out = append(out, p.Samples...)
			total += wavDurationMs(p)
		case "silence":
			n := sr * ch * 2 * e.DurationMs / 1000
			if n < 0 {
				n = 0
			}
			out = append(out, make([]byte, n)...)
			total += e.DurationMs
		default:
			return nil, 0, http.StatusBadRequest, "未知 stream type"
		}
	}
	return out, total, 0, ""
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		InstanceID string `json:"instance_id"`
		TurnID     string `json:"turn_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if body.InstanceID == "" {
		writeErr(w, http.StatusBadRequest, "缺 instance_id")
		return
	}
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok || d.instanceID != body.InstanceID || d.inst == nil {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "generation_gone")
		return
	}
	inst := d.inst
	s.mu.Unlock()

	res, err := inst.Interrupt(body.TurnID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	out := map[string]any{
		"interrupted": res.Interrupted,
		"device_id":   id,
		"instance_id": body.InstanceID,
	}
	s.mu.Lock()
	if dd, ok := s.devices[id]; ok {
		dd.lastActivity = time.Now()
		if res.Interrupted {
			if tr, ok := dd.turns[res.TurnID]; ok {
				tr.EndReason = res.EndReason
				tr.UplinkReason = res.UplinkReason
				tr.ReplyKind = res.ReplyKind
			}
		}
	}
	s.mu.Unlock()
	if res.Interrupted {
		out["turn_id"] = res.TurnID
		out["turn_end_reason"] = res.EndReason
		out["uplink_end_reason"] = res.UplinkReason
		out["reply_kind"] = res.ReplyKind
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		PlayingMode *int `json:"playingMode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok || d.inst == nil {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	inst := d.inst
	s.mu.Unlock()
	seq, err := inst.ManualReport(body.PlayingMode)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.mu.Lock()
	if dd, ok := s.devices[id]; ok {
		dd.lastActivity = time.Now()
		if body.PlayingMode != nil {
			dd.playingMode = *body.PlayingMode
			dd.cfg.PlayingMode = *body.PlayingMode
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{"sequence_number": seq})
}
