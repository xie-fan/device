package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"toy-device-simulator/core"
)

func newAssetID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ast_" + hex.EncodeToString(b[:])
}

func (s *Server) handlePostAsset(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart 解析失败")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少 file 字段")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取失败")
		return
	}
	_ = hdr
	// Phase 4c raw PCM：sample_rate/channels/sample_format 三项全给才按 raw 收，
	// 服务端包 WAV 头后与普通上传走同一管线；缺一项 → 400。
	if data, err = maybeWrapRawPCM(r, data); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	maxBytes := s.opts.Config.MaxAssetBytes
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		writeErr(w, http.StatusBadRequest, "超过 max_asset_bytes")
		return
	}
	pcm, err := core.DecodeWAV(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "非 WAV")
		return
	}
	durMs := wavDurationMs(pcm)
	if maxDur := s.opts.Config.MaxAssetDurationSec; maxDur > 0 && durMs > maxDur*1000 {
		writeErr(w, http.StatusBadRequest, "超过 max_asset_duration_sec")
		return
	}
	id := newAssetID()
	root := s.opts.Config.AssetsRoot
	if root == "" {
		root = filepath.Join(os.TempDir(), "toy-assets")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := filepath.Join(root, id+".wav")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	obj := &assetObj{
		id:           id,
		path:         path,
		epoch:        1,
		bytes:        len(data),
		durationMs:   durMs,
		sampleRate:   pcm.SampleRate,
		channels:     pcm.Channels,
		sampleFormat: "s16le",
	}
	s.assetMu.Lock()
	s.assets[id] = obj
	s.assetMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"asset_id":      id,
		"bytes":         obj.bytes,
		"duration_ms":   obj.durationMs,
		"sample_rate":   obj.sampleRate,
		"channels":      obj.channels,
		"sample_format": obj.sampleFormat,
		"container":     "wav",
		"epoch":         obj.epoch,
	})
}

// maybeWrapRawPCM 判定 raw PCM 上传：三项 fmt 全给 → 校验并包 WAV 头；
// 全没给 → 原样返回（WAV 路径）；只给一部分 → 报错。
func maybeWrapRawPCM(r *http.Request, data []byte) ([]byte, error) {
	srS, chS, sf := r.FormValue("sample_rate"), r.FormValue("channels"), r.FormValue("sample_format")
	given := 0
	for _, v := range []string{srS, chS, sf} {
		if v != "" {
			given++
		}
	}
	if given == 0 {
		return data, nil
	}
	if given < 3 {
		return nil, fmt.Errorf("raw PCM 须同时给 sample_rate/channels/sample_format")
	}
	if sf != "s16le" {
		return nil, fmt.Errorf("sample_format 仅支持 s16le")
	}
	sr, err := strconv.Atoi(srS)
	if err != nil || sr <= 0 {
		return nil, fmt.Errorf("sample_rate 非法")
	}
	ch, err := strconv.Atoi(chS)
	if err != nil || ch <= 0 {
		return nil, fmt.Errorf("channels 非法")
	}
	if len(data) >= 4 && string(data[0:4]) == "RIFF" {
		return nil, fmt.Errorf("raw PCM 模式不接受 RIFF/WAV 内容")
	}
	if len(data)%(ch*2) != 0 {
		return nil, fmt.Errorf("raw PCM 长度须为帧大小（channels*2 字节）的整数倍")
	}
	return core.EncodeWAV(core.PCM{
		Samples: data, SampleRate: sr, Channels: ch, BitsPerSample: 16,
	}), nil
}

func wavDurationMs(p core.PCM) int {
	bps := p.SampleRate * p.Channels * (p.BitsPerSample / 8)
	if bps <= 0 {
		return 0
	}
	return len(p.Samples) * 1000 / bps
}

func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.assetMu.Lock()
	a, ok := s.assets[id]
	s.assetMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "asset 不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"asset_id": a.id, "bytes": a.bytes, "duration_ms": a.durationMs,
		"sample_rate": a.sampleRate, "channels": a.channels,
		"sample_format": a.sampleFormat, "container": "wav", "epoch": a.epoch,
	})
}

func (s *Server) handleGetAssetContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.assetMu.Lock()
	a, ok := s.assets[id]
	path := ""
	if ok {
		path = a.path
	}
	s.assetMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "asset 不存在")
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "文件不存在")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.assetMu.Lock()
	a, ok := s.assets[id]
	if ok {
		a.epoch++
		_ = os.Remove(a.path)
		delete(s.assets, id)
	}
	s.assetMu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// copyAsset 短锁读 path+epoch → AfterAssetStat → 无锁读盘 → 短锁复验。
func (s *Server) copyAsset(id string) ([]byte, *assetObj, error) {
	s.assetMu.Lock()
	a, ok := s.assets[id]
	if !ok {
		s.assetMu.Unlock()
		return nil, nil, fmt.Errorf("not found")
	}
	path, epoch := a.path, a.epoch
	s.assetMu.Unlock()

	if s.opts.AfterAssetStat != nil {
		s.opts.AfterAssetStat()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("not found")
	}
	s.assetMu.Lock()
	a2, ok := s.assets[id]
	if !ok || a2.epoch != epoch {
		s.assetMu.Unlock()
		return nil, nil, fmt.Errorf("not found")
	}
	s.assetMu.Unlock()
	return data, a2, nil
}
