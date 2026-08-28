package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
	"toy-device-simulator/media"
)

// assetObj 音频库条目。Phase 5c：持久化 + name/language/format 元数据 +
// 按目标规格的转码派生副本缓存（variants，不持久化，重启后按需重建）。
type assetObj struct {
	id           string
	path         string
	epoch        int
	bytes        int
	durationMs   int
	sampleRate   int
	channels     int
	sampleFormat string // wav 资产为 s16le；压缩格式无意义（空）
	name         string
	language     string
	format       string // wav/mp3/amr/aac
	bitrateKbps  float64
	createdAt    int64             // unix ms
	variants     map[string]string // 规格指纹 → 派生文件路径
}

func newAssetID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ast_" + hex.EncodeToString(b[:])
}

func extByFormat(f string) string {
	switch f {
	case media.FormatMP3:
		return ".mp3"
	case media.FormatAMR:
		return ".amr"
	case media.FormatAAC:
		return ".aac"
	default:
		return ".wav"
	}
}

func mimeByFormat(f string) string {
	switch f {
	case media.FormatMP3:
		return "audio/mpeg"
	case media.FormatAMR:
		return "audio/amr"
	case media.FormatAAC:
		return "audio/aac"
	default:
		return "audio/wav"
	}
}

// assetTargetSpec 设备 audio 配置 → 资产目标规格。
// 设备 format=pcm 在资产层等价 wav（统一带容器落盘，发送时才剥头）。
func assetTargetSpec(a config.Audio) media.Spec {
	f := a.Format
	if f == media.FormatPCM {
		f = media.FormatWAV
	}
	return media.Spec{Format: f, SampleRate: a.SampleRate, Channels: a.Channels, BitrateKbps: a.BitrateKbps}
}

// assetMatchesSpec 资产元数据是否满足目标规格（wav 还须为 s16le 编码，
// 否则 DecodeWAV 无法进 PCM 管线，视为需转码）。
func assetMatchesSpec(format string, sampleRate, channels int, sampleFormat string, spec media.Spec) bool {
	if format != spec.Format || sampleRate != spec.SampleRate || channels != spec.Channels {
		return false
	}
	if spec.Format == media.FormatWAV && sampleFormat != "s16le" {
		return false
	}
	return true
}

func specFP(spec media.Spec) string {
	return fmt.Sprintf("%s_%d_%d_%g", spec.Format, spec.SampleRate, spec.Channels, spec.BitrateKbps)
}

// ---- 持久化 ----

type assetIndexEntry struct {
	ID           string  `json:"id"`
	File         string  `json:"file"`
	Name         string  `json:"name"`
	Language     string  `json:"language"`
	Format       string  `json:"format"`
	Bytes        int     `json:"bytes"`
	DurationMs   int     `json:"duration_ms"`
	SampleRate   int     `json:"sample_rate"`
	Channels     int     `json:"channels"`
	SampleFormat string  `json:"sample_format"`
	BitrateKbps  float64 `json:"bitrate_kbps"`
	CreatedAt    int64   `json:"created_at"`
}

type assetIndexFile struct {
	Assets []assetIndexEntry `json:"assets"`
}

func (s *Server) assetsRoot() string {
	root := s.opts.Config.AssetsRoot
	if root == "" {
		root = filepath.Join(os.TempDir(), "toy-assets")
	}
	return root
}

// loadAssetIndex 启动时从 index.json 重建库（api.New 调用，无并发）。
// 顺带清掉孤儿派生副本与残留临时文件（variants 不持久化）。
func (s *Server) loadAssetIndex() {
	root := s.assetsRoot()
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			n := e.Name()
			if strings.Contains(n, ".v.") || strings.HasPrefix(n, ".tmp-") {
				_ = os.Remove(filepath.Join(root, n))
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		return
	}
	var idx assetIndexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		return
	}
	for _, e := range idx.Assets {
		p := filepath.Join(root, e.File)
		if _, err := os.Stat(p); err != nil {
			continue // 文件丢了就丢条目
		}
		s.assets[e.ID] = &assetObj{
			id: e.ID, path: p, epoch: 1,
			bytes: e.Bytes, durationMs: e.DurationMs,
			sampleRate: e.SampleRate, channels: e.Channels, sampleFormat: e.SampleFormat,
			name: e.Name, language: e.Language, format: e.Format,
			bitrateKbps: e.BitrateKbps, createdAt: e.CreatedAt,
			variants: map[string]string{},
		}
	}
}

// persistAssetIndexLocked 重写 index.json；调用方必须持 assetMu。
func (s *Server) persistAssetIndexLocked() {
	idx := assetIndexFile{Assets: make([]assetIndexEntry, 0, len(s.assets))}
	for _, a := range s.assets {
		idx.Assets = append(idx.Assets, assetIndexEntry{
			ID: a.id, File: filepath.Base(a.path), Name: a.name, Language: a.language,
			Format: a.format, Bytes: a.bytes, DurationMs: a.durationMs,
			SampleRate: a.sampleRate, Channels: a.channels, SampleFormat: a.sampleFormat,
			BitrateKbps: a.bitrateKbps, CreatedAt: a.createdAt,
		})
	}
	sort.Slice(idx.Assets, func(i, j int) bool { return idx.Assets[i].ID < idx.Assets[j].ID })
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.assetsRoot(), "index.json"), b, 0o644)
}

// ---- 上传/导入 ----

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

	name := r.FormValue("name")
	if name == "" && hdr != nil {
		name = hdr.Filename
	}
	language := r.FormValue("language")

	// device_id 给定 → 上传即按该设备规格转码（格式一致则直存）。
	var target *media.Spec
	if devID := r.FormValue("device_id"); devID != "" {
		s.mu.Lock()
		d, ok := s.devices[devID]
		if ok {
			spec := assetTargetSpec(d.cfg.Audio)
			target = &spec
		}
		s.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusNotFound, "device 不存在")
			return
		}
	}

	root := s.assetsRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := newAssetID()
	tmp := filepath.Join(root, ".tmp-"+id)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp)

	// 探测：有 ffmpeg 用 ffprobe（任意格式）；无则仅支持 WAV。
	var info media.Info
	if s.opts.Media != nil {
		info, err = s.opts.Media.Probe(r.Context(), tmp)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "无法识别的音频："+err.Error())
			return
		}
	} else {
		pcm, derr := core.DecodeWAV(data)
		if derr != nil {
			writeErr(w, http.StatusBadRequest, "非 WAV（无 ffmpeg 时仅支持 WAV/raw PCM）")
			return
		}
		info = media.Info{
			Format: media.FormatWAV, Codec: "pcm_s16le",
			SampleRate: pcm.SampleRate, Channels: pcm.Channels,
			DurationMs: wavDurationMs(pcm),
		}
	}

	transcoded := false
	final := ""
	sampleFormat := ""
	if info.Format == media.FormatWAV && info.Codec == "pcm_s16le" {
		sampleFormat = "s16le"
	}
	if target != nil && !assetMatchesSpec(info.Format, info.SampleRate, info.Channels, sampleFormat, *target) {
		if s.opts.Media == nil {
			writeErr(w, http.StatusBadRequest, "格式与设备不符且无 ffmpeg 可转码")
			return
		}
		if err := s.opts.Media.CanEncode(target.Format, target.SampleRate); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		final = filepath.Join(root, id+extByFormat(target.Format))
		if err := s.opts.Media.TranscodeFile(r.Context(), tmp, final, *target); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		pinfo, perr := s.opts.Media.Probe(r.Context(), final)
		if perr != nil {
			_ = os.Remove(final)
			writeErr(w, http.StatusInternalServerError, "转码产物探测失败："+perr.Error())
			return
		}
		info = pinfo
		info.Format = target.Format // pcm 裸流探不出容器名，以目标为准
		transcoded = true
	} else {
		final = filepath.Join(root, id+extByFormat(info.Format))
		if err := os.Rename(tmp, final); err != nil {
			// tmp 与 final 跨卷或被占用时退化为拷贝。
			if werr := os.WriteFile(final, data, 0o644); werr != nil {
				writeErr(w, http.StatusInternalServerError, werr.Error())
				return
			}
		}
	}
	sampleFormat = ""
	if info.Format == media.FormatWAV {
		sampleFormat = "s16le"
	}

	if maxDur := s.opts.Config.MaxAssetDurationSec; maxDur > 0 && info.DurationMs > maxDur*1000 {
		_ = os.Remove(final)
		writeErr(w, http.StatusBadRequest, "超过 max_asset_duration_sec")
		return
	}
	st, err := os.Stat(final)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	obj := &assetObj{
		id: id, path: final, epoch: 1,
		bytes: int(st.Size()), durationMs: info.DurationMs,
		sampleRate: info.SampleRate, channels: info.Channels, sampleFormat: sampleFormat,
		name: name, language: language, format: info.Format,
		bitrateKbps: info.BitrateKbps, createdAt: time.Now().UnixMilli(),
		variants: map[string]string{},
	}
	s.assetMu.Lock()
	s.assets[id] = obj
	s.persistAssetIndexLocked()
	s.assetMu.Unlock()
	resp := assetPublic(obj)
	resp["transcoded"] = transcoded
	writeJSON(w, http.StatusCreated, resp)
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

// ---- 查询/管理 ----

func assetPublic(a *assetObj) map[string]any {
	return map[string]any{
		"asset_id": a.id, "bytes": a.bytes, "duration_ms": a.durationMs,
		"sample_rate": a.sampleRate, "channels": a.channels,
		"sample_format": a.sampleFormat, "container": a.format,
		"format": a.format, "name": a.name, "language": a.language,
		"bitrate_kbps": a.bitrateKbps, "created_at": a.createdAt, "epoch": a.epoch,
	}
}

func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	fmtQ := r.URL.Query().Get("format")
	langQ := r.URL.Query().Get("language")
	s.assetMu.Lock()
	list := make([]*assetObj, 0, len(s.assets))
	for _, a := range s.assets {
		if fmtQ != "" && a.format != fmtQ {
			continue
		}
		if langQ != "" && a.language != langQ {
			continue
		}
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].createdAt != list[j].createdAt {
			return list[i].createdAt < list[j].createdAt
		}
		return list[i].id < list[j].id
	})
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, assetPublic(a))
	}
	s.assetMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"assets": out})
}

func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.assetMu.Lock()
	a, ok := s.assets[id]
	var resp map[string]any
	if ok {
		resp = assetPublic(a)
	}
	s.assetMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "asset 不存在")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePatchAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if err := rejectUnknownKeys(raw, map[string]bool{"name": true, "language": true}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var name, language *string
	if v, ok := raw["name"]; ok {
		sv, ok := v.(string)
		if !ok {
			writeErr(w, http.StatusBadRequest, "name 类型非法")
			return
		}
		name = &sv
	}
	if v, ok := raw["language"]; ok {
		sv, ok := v.(string)
		if !ok {
			writeErr(w, http.StatusBadRequest, "language 类型非法")
			return
		}
		language = &sv
	}
	s.assetMu.Lock()
	a, ok := s.assets[id]
	var resp map[string]any
	if ok {
		if name != nil {
			a.name = *name
		}
		if language != nil {
			a.language = *language
		}
		s.persistAssetIndexLocked()
		resp = assetPublic(a)
	}
	s.assetMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "asset 不存在")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetAssetContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.assetMu.Lock()
	a, ok := s.assets[id]
	path, format := "", ""
	if ok {
		path, format = a.path, a.format
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
	w.Header().Set("Content-Type", mimeByFormat(format))
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
		for _, vp := range a.variants {
			_ = os.Remove(vp)
		}
		delete(s.assets, id)
		s.persistAssetIndexLocked()
	}
	s.assetMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// ---- speak 侧解析（含选用时自动转码缓存） ----

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

// resolveAssetFile 返回满足 spec 的文件路径（原文件或转码派生副本，
// 副本按规格指纹缓存复用）。资产不存在 → (404)；需转码而无 ffmpeg → (400)。
func (s *Server) resolveAssetFile(ctx context.Context, id string, spec media.Spec) (path string, durMs, code int, msg string) {
	s.assetMu.Lock()
	a, ok := s.assets[id]
	if !ok {
		s.assetMu.Unlock()
		return "", 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
	}
	if assetMatchesSpec(a.format, a.sampleRate, a.channels, a.sampleFormat, spec) {
		p, d := a.path, a.durationMs
		s.assetMu.Unlock()
		return p, d, 0, ""
	}
	fp := specFP(spec)
	if vp, ok := a.variants[fp]; ok {
		d := a.durationMs
		s.assetMu.Unlock()
		return vp, d, 0, ""
	}
	src, epoch, dur := a.path, a.epoch, a.durationMs
	s.assetMu.Unlock()

	if s.opts.Media == nil {
		return "", 0, http.StatusBadRequest, "asset 格式与设备不符且无 ffmpeg 可转码"
	}
	if err := s.opts.Media.CanEncode(spec.Format, spec.SampleRate); err != nil {
		return "", 0, http.StatusBadRequest, err.Error()
	}
	vpath := filepath.Join(s.assetsRoot(), id+".v."+fp+extByFormat(spec.Format))
	part := vpath + ".part-" + newAssetID()[4:]
	if err := s.opts.Media.TranscodeFile(ctx, src, part, spec); err != nil {
		return "", 0, http.StatusBadRequest, "转码失败："+err.Error()
	}
	if err := os.Rename(part, vpath); err != nil {
		// 并发转码输家：对方已就位则用对方的，删掉自己的半成品。
		if _, serr := os.Stat(vpath); serr != nil {
			_ = os.Remove(part)
			return "", 0, http.StatusInternalServerError, err.Error()
		}
		_ = os.Remove(part)
	}
	s.assetMu.Lock()
	a2, ok := s.assets[id]
	if !ok || a2.epoch != epoch {
		s.assetMu.Unlock()
		_ = os.Remove(vpath)
		return "", 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
	}
	a2.variants[fp] = vpath
	s.assetMu.Unlock()
	return vpath, dur, 0, ""
}

// resolveAssetWAV speak（pcm/wav 设备）用：任意格式资产 → 设备采样率/声道的
// s16le PCM（格式不符时经 ffmpeg 转出 wav 派生副本）。
func (s *Server) resolveAssetWAV(ctx context.Context, id string, sr, ch int) (core.PCM, int, int, string) {
	// 快路径：已是匹配的 wav 时沿用 copyAsset 的 epoch 复验语义。
	s.assetMu.Lock()
	a, ok := s.assets[id]
	matched := ok && assetMatchesSpec(a.format, a.sampleRate, a.channels, a.sampleFormat, media.Spec{Format: media.FormatWAV, SampleRate: sr, Channels: ch})
	s.assetMu.Unlock()
	if !ok {
		return core.PCM{}, 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
	}
	if matched {
		raw, _, err := s.copyAsset(id)
		if err != nil {
			return core.PCM{}, 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
		}
		p, derr := core.DecodeWAV(raw)
		if derr != nil {
			return core.PCM{}, 0, http.StatusBadRequest, "非 WAV"
		}
		return p, wavDurationMs(p), 0, ""
	}
	spec := media.Spec{Format: media.FormatWAV, SampleRate: sr, Channels: ch}
	path, _, code, msg := s.resolveAssetFile(ctx, id, spec)
	if code != 0 {
		return core.PCM{}, 0, code, msg
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return core.PCM{}, 0, http.StatusNotFound, "asset 不存在或 epoch 已变"
	}
	p, derr := core.DecodeWAV(raw)
	if derr != nil {
		return core.PCM{}, 0, http.StatusInternalServerError, "转码产物非 WAV"
	}
	return p, wavDurationMs(p), 0, ""
}
