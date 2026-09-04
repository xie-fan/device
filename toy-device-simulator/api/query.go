package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"toy-device-simulator/core"
	"toy-device-simulator/media"
)

func (s *Server) requireInstance(r *http.Request) (string, bool) {
	ins := r.URL.Query().Get("instance_id")
	return ins, ins != ""
}

func (s *Server) purgeExpiredTombsLocked() {
	now := time.Now()
	for id, t := range s.tombs {
		if now.Before(t.expires) {
			continue
		}
		s.removeInstanceRecordingsLocked(t)
		delete(s.tombs, id)
	}
}

func (s *Server) removeInstanceRecordingsLocked(t *tombstone) {
	if t == nil {
		return
	}
	out := t.cfg.Recording.OutputDir
	if out == "" {
		out = s.opts.RecordingsDir
	}
	dir, err := core.RecordingInstanceDir(out, t.deviceID, t.instanceID)
	if err != nil {
		return
	}
	_ = os.RemoveAll(dir)
}

func (s *Server) handleListTurns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ins, ok := s.requireInstance(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺 instance_id")
		return
	}
	v := s.resolveInstance(id, ins)
	if v.src == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	list := make([]map[string]any, 0, len(v.turns))
	for i := range v.turns {
		list = append(list, turnJSON(&v.turns[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"turns": list, "source": v.src})
}

func turnJSON(t *turnRec) map[string]any {
	return map[string]any{
		"turn_id": t.TurnID, "instance_id": t.InstanceID,
		"uplink_uuid": t.UplinkUUID, "seq_before": t.SeqBefore,
		"turn_end_reason": t.EndReason, "uplink_end_reason": t.UplinkReason,
		"reply_kind": t.ReplyKind,
	}
}

func (s *Server) handleGetTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turnID := r.PathValue("turn_id")
	ins, ok := s.requireInstance(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺 instance_id")
		return
	}
	v := s.resolveInstance(id, ins)
	if v.src == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	tr := v.turn(turnID)
	if tr == nil {
		writeErr(w, http.StatusNotFound, "turn 不存在")
		return
	}
	writeJSON(w, http.StatusOK, turnJSON(tr))
}

func (s *Server) handleGetFrames(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turnID := r.PathValue("turn_id")
	ins, ok := s.requireInstance(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺 instance_id")
		return
	}
	v := s.resolveInstance(id, ins)
	if v.src == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	outDir := v.outDir
	if tr := v.turn(turnID); tr != nil && tr.OutputDir != "" {
		outDir = tr.OutputDir
	}
	frames, _, _, _, err := core.RecordingPathsPhase2(outDir, id, ins, turnID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := os.ReadFile(frames)
	if err != nil {
		writeErr(w, http.StatusNotFound, "frames 不存在")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) handleGetAudioUplink(w http.ResponseWriter, r *http.Request) {
	s.serveAudio(w, r, true)
}

func (s *Server) handleGetAudioDownlink(w http.ResponseWriter, r *http.Request) {
	s.serveAudio(w, r, false)
}

func (s *Server) serveAudio(w http.ResponseWriter, r *http.Request, uplink bool) {
	id := r.PathValue("id")
	turnID := r.PathValue("turn_id")
	ins, ok := s.requireInstance(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺 instance_id")
		return
	}
	v := s.resolveInstance(id, ins)
	if v.src == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	outDir := v.outDir
	sr, ch := v.audio.SampleRate, v.audio.Channels
	if sr <= 0 {
		sr, ch = 16000, 1
	}
	// 上行格式优先用 turn.json 自己记的（Phase 8）：跨重启回看时设备的当前配置
	// 未必还是录这段时的那一套，设备甚至可能已经删了。
	devFormat := v.audio.Format
	if tr := v.turn(turnID); tr != nil {
		if tr.SampleRate > 0 {
			sr, ch = tr.SampleRate, tr.Channels
		}
		if tr.OutputDir != "" {
			outDir = tr.OutputDir
		}
		if tr.UpFormat != "" {
			devFormat = tr.UpFormat
		}
	}
	_, up, down, turnPath, err := core.RecordingPathsPhase2(outDir, id, ins, turnID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path := up
	if !uplink {
		path = down
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "录音文件不存在")
		return
	}

	// Phase 5e 格式判定：上行内容=发出的帧 payload 拼接 → 格式即设备 format；
	// 下行以 turn.json 记录的首帧头格式为准，缺省按设备 format 推断
	// （压缩设备→设备格式；pcm/wav 设备的下行历史均为裸 PCM）。
	format := devFormat
	if !uplink {
		df, dsr := readTurnDownMeta(turnPath)
		switch {
		case df != "":
			format = df
			if dsr > 0 {
				sr = dsr
			}
		case media.Compressed(devFormat):
			format = devFormat
		default:
			format = media.FormatPCM
		}
	}
	rawMode := r.URL.Query().Get("raw") == "1"
	// 真实服务端的 AMR 下行是「每包一个独立文件」——包首都带存储头，原样追加
	// 落盘后文件里就有多个头。解码前把后续的头剥掉（ffmpeg 会把它们当成坏帧
	// 吃进去，每个多出约 20ms 杂音）。落盘契约不变，raw 仍给真实字节。
	if !uplink && !rawMode && format == media.FormatAMR {
		data = stripRepeatedAMRHeaders(data)
	}
	switch {
	case format == media.FormatPCM || format == "":
		if rawMode {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		wav := core.EncodeWAV(core.PCM{Samples: data, SampleRate: sr, Channels: ch, BitsPerSample: 16})
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wav)
	case format == media.FormatWAV:
		// wav 设备的上行流本身就是完整 RIFF 文件，原样即可播。
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	default:
		if rawMode {
			w.Header().Set("Content-Type", mimeByFormat(format))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		if s.opts.Media == nil {
			writeErr(w, http.StatusBadRequest, "压缩格式试听需要 ffmpeg；可用 ?raw=1 取原始字节")
			return
		}
		wav, terr := s.transcodeBytesToWAV(r.Context(), data, format)
		if terr != nil {
			writeErr(w, http.StatusInternalServerError, "解码失败："+terr.Error())
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wav)
	}
}

// readTurnDownMeta 读 turn.json（JSONL，取最后一行）的下行格式记录。
func readTurnDownMeta(turnPath string) (format string, sampleRate int) {
	raw, err := os.ReadFile(turnPath)
	if err != nil {
		return "", 0
	}
	var row struct {
		DownFormat     string `json:"down_format"`
		DownSampleRate int    `json:"down_sample_rate"`
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) == 0 {
		return "", 0
	}
	if err := json.Unmarshal(lines[len(lines)-1], &row); err != nil {
		return "", 0
	}
	return row.DownFormat, row.DownSampleRate
}

// transcodeBytesToWAV 压缩字节 → wav（试听用；不重采样，保留源参数）。
func (s *Server) transcodeBytesToWAV(ctx context.Context, data []byte, format string) ([]byte, error) {
	src := filepath.Join(os.TempDir(), "tds-play-"+newAssetID()[4:]+extByFormat(format))
	if err := os.WriteFile(src, data, 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(src)
	dst := src + ".wav"
	defer os.Remove(dst)
	if err := s.opts.Media.TranscodeFile(ctx, src, dst, media.Spec{Format: media.FormatWAV}); err != nil {
		return nil, err
	}
	return os.ReadFile(dst)
}

func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ins, ok := s.requireInstance(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "缺 instance_id")
		return
	}
	after := 0
	if q := r.URL.Query().Get("after_event_seq"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "after_event_seq 非法")
			return
		}
		after = n
	}
	v := s.resolveInstance(id, ins)
	if v.src == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	if v.src == "disk" {
		writeJSON(w, http.StatusOK, map[string]any{"events": diskEvents(v.dir, after), "source": "disk"})
		return
	}
	log := v.log
	if log.CursorExpired(after) {
		writeJSON(w, http.StatusGone, map[string]any{
			"error":               "event_seq_expired",
			"evicted_through_seq": log.EvictedThrough(),
			"oldest_seq":          log.OldestSeq(),
			"newest_seq":          log.NewestSeq(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": log.After(after)})
}

// AMR 存储头（RFC 4867 §5）；下行按包重复出现，合并回一个文件时只留第一个。
var amrStorageHeaders = [][]byte{[]byte("#!AMR-WB\n"), []byte("#!AMR\n")}

// stripRepeatedAMRHeaders 保留首个存储头，删掉其余所有出现。
func stripRepeatedAMRHeaders(data []byte) []byte {
	var hdr []byte
	for _, h := range amrStorageHeaders {
		if bytes.HasPrefix(data, h) {
			hdr = h
			break
		}
	}
	if hdr == nil {
		return data
	}
	body := data[len(hdr):]
	if !bytes.Contains(body, hdr) {
		return data
	}
	return append(append([]byte(nil), hdr...), bytes.ReplaceAll(body, hdr, nil)...)
}
