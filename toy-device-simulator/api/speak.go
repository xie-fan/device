package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"toy-device-simulator/core"
	"toy-device-simulator/media"
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
	devFormat := d.cfg.Audio.Format
	devBitrate := d.cfg.Audio.BitrateKbps
	devSliceMs := d.cfg.Audio.SliceMs
	s.mu.Unlock()

	// Phase 5d：压缩格式设备（mp3/amr/aac）走 ffmpeg -re 流式上行；
	// pcm/wav 保持原有预构建帧管线。
	var (
		pcm        []byte
		durMs      int
		streamOpen core.StreamOpen
		chunkBytes int
	)
	if media.Compressed(devFormat) {
		if hasStream {
			writeErr(w, http.StatusBadRequest, "压缩格式设备暂不支持 stream 拼接，请用 asset_id")
			return
		}
		if s.opts.Media == nil {
			writeErr(w, http.StatusBadRequest, "压缩格式上行需要 ffmpeg（未检测到）")
			return
		}
		// 归一化码率：0 → 格式默认，amr 就近合法档位（与转码规格指纹一致）。
		bitrate := media.NormalizeBitrate(devFormat, sr, devBitrate)
		spec := media.Spec{Format: devFormat, SampleRate: sr, Channels: ch, BitrateKbps: bitrate}
		path, dur, code, msg := s.resolveAssetFile(r.Context(), body.AssetID, spec)
		if code != 0 {
			writeErr(w, code, msg)
			return
		}
		durMs = dur
		tc := s.opts.Media
		// 资产已满足设备规格（resolveAssetFile 保证）→ -c copy 仅限速直通。
		streamOpen = func(ctx context.Context) (io.ReadCloser, error) {
			return tc.StreamRealtime(ctx, path, spec, true)
		}
		if devSliceMs <= 0 {
			devSliceMs = 100
		}
		chunkBytes = int(bitrate * 1000 / 8 * float64(devSliceMs) / 1000)
	} else {
		var errCode int
		var errMsg string
		pcm, durMs, errCode, errMsg = s.buildSpeakPCM(r.Context(), body, sr, ch, sf)
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

	var res core.SpeakResult
	var err error
	if streamOpen != nil {
		res, err = inst.SpeakStreamPermit(streamOpen, durMs, chunkBytes, s.tryAcquireSpeak)
	} else {
		res, err = inst.SpeakPermit(pcm, s.tryAcquireSpeak)
	}
	if err != nil {
		if errors.Is(err, core.ErrSpeakPermit) {
			writeErr(w, http.StatusTooManyRequests, "speak_permit")
			return
		}
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	turnID := res.TurnID

	s.mu.Lock()
	if dd, ok := s.devices[id]; ok {
		tr := dd.turns[turnID]
		if tr == nil {
			tr = &turnRec{TurnID: turnID}
			dd.turns[turnID] = tr
		}
		tr.InstanceID = ins
		tr.SampleRate = sr
		tr.Channels = ch
		tr.OutputDir = inst.Config().Recording.OutputDir
		tr.UpFormat = devFormat
		if !res.Queued {
			// 排队项的 uuid/seq_before 由 OnTurnStarted 出队时补。
			tr.UplinkUUID = res.UUID
			tr.SeqBefore = res.SeqBefore
		}
		dd.lastActivity = time.Now()
	}
	s.mu.Unlock()

	resp := map[string]any{
		"turn_id": turnID, "instance_id": ins,
	}
	if res.Queued {
		resp["queued"] = true
		resp["queue_position"] = res.QueuePos
	} else {
		resp["uplink_uuid"] = res.UUID
		resp["seq_before"] = res.SeqBefore
	}
	if !wait {
		writeJSON(w, http.StatusAccepted, resp)
		return
	}
	timeout := inst.WaitBudgetFor(len(pcm))
	if streamOpen != nil {
		timeout = inst.WaitBudgetForDuration(durMs)
	}
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
			tr.DownFormat = ev.DownFormat
			tr.DownBytes = ev.DownBytes
			if ev.UpFormat != "" {
				tr.UpFormat = ev.UpFormat
			}
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

// buildSpeakPCM 组装上行 PCM。Phase 5c：资产格式与设备不符时经 ffmpeg
// 转码派生副本（缓存复用），任意入库格式均可喂 pcm/wav 设备。
func (s *Server) buildSpeakPCM(ctx context.Context, body speakBody, sr, ch int, sf string) (pcm []byte, durMs, code int, msg string) {
	_ = sf // 设备 sample_format 固定 s16le，解析统一产出 s16le
	if body.AssetID != "" {
		p, dur, errCode, errMsg := s.resolveAssetWAV(ctx, body.AssetID, sr, ch)
		if errCode != 0 {
			return nil, 0, errCode, errMsg
		}
		return p.Samples, dur, 0, ""
	}
	var out []byte
	total := 0
	for _, e := range body.Stream {
		switch e.Type {
		case "audio":
			p, dur, errCode, errMsg := s.resolveAssetWAV(ctx, e.AssetID, sr, ch)
			if errCode != 0 {
				return nil, 0, errCode, errMsg
			}
			out = append(out, p.Samples...)
			total += dur
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
