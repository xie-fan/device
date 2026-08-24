package api

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"toy-device-simulator/core"
)

func (s *Server) requireInstance(r *http.Request) (string, bool) {
	ins := r.URL.Query().Get("instance_id")
	return ins, ins != ""
}

func (s *Server) resolveInstance(deviceID, instanceID string) (live *managedDevice, tomb *tombstone, found string) {
	s.purgeExpiredTombsLocked()
	if d, ok := s.devices[deviceID]; ok && d.instanceID == instanceID {
		return d, nil, "live"
	}
	if t, ok := s.tombs[instanceID]; ok && t.deviceID == deviceID && time.Now().Before(t.expires) {
		return nil, t, "tomb"
	}
	return nil, nil, ""
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
	s.mu.Lock()
	defer s.mu.Unlock()
	live, tomb, found := s.resolveInstance(id, ins)
	if found == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	turns := map[string]*turnRec{}
	if live != nil {
		turns = live.turns
	} else {
		turns = tomb.turns
	}
	list := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		list = append(list, turnJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"turns": list})
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
	s.mu.Lock()
	defer s.mu.Unlock()
	live, tomb, found := s.resolveInstance(id, ins)
	if found == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	var tr *turnRec
	if live != nil {
		tr = live.turns[turnID]
	} else {
		tr = tomb.turns[turnID]
	}
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
	s.mu.Lock()
	live, tomb, found := s.resolveInstance(id, ins)
	if found == "" {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	outDir := s.opts.RecordingsDir
	var tr *turnRec
	if live != nil {
		outDir = live.cfg.Recording.OutputDir
		tr = live.turns[turnID]
	} else {
		outDir = tomb.cfg.Recording.OutputDir
		tr = tomb.turns[turnID]
	}
	if tr != nil && tr.OutputDir != "" {
		outDir = tr.OutputDir
	}
	s.mu.Unlock()
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
	s.mu.Lock()
	live, tomb, found := s.resolveInstance(id, ins)
	if found == "" {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	outDir := s.opts.RecordingsDir
	sr, ch := 16000, 1
	var tr *turnRec
	if live != nil {
		outDir = live.cfg.Recording.OutputDir
		sr, ch = live.cfg.Audio.SampleRate, live.cfg.Audio.Channels
		tr = live.turns[turnID]
	} else {
		outDir = tomb.cfg.Recording.OutputDir
		sr, ch = tomb.cfg.Audio.SampleRate, tomb.cfg.Audio.Channels
		tr = tomb.turns[turnID]
	}
	if tr != nil {
		if tr.SampleRate > 0 {
			sr, ch = tr.SampleRate, tr.Channels
		}
		if tr.OutputDir != "" {
			outDir = tr.OutputDir
		}
	}
	s.mu.Unlock()
	_, up, down, _, err := core.RecordingPathsPhase2(outDir, id, ins, turnID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path := up
	if !uplink {
		path = down
	}
	pcm, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "录音文件不存在")
		return
	}
	wav := core.EncodeWAV(core.PCM{Samples: pcm, SampleRate: sr, Channels: ch, BitsPerSample: 16})
	w.Header().Set("Content-Type", "audio/wav")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wav)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	live, tomb, found := s.resolveInstance(id, ins)
	if found == "" {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	var log *core.EventLog
	if live != nil {
		log = live.log
	} else {
		log = tomb.log
	}
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
