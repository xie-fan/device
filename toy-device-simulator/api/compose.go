package api

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"toy-device-simulator/core"
)

// 组合素材（Phase 15）：几条音频各自去掉首尾静音、首尾相接拼成一条连续的新素材，
// 比如「你好」+「今天星期几」+「讲个故事」→ 一句话里问几件事。存成普通素材，所以
// 任意设备格式都能送（amr 照常转码）、能试听、能复现；speak 的 stream 拼接做不到这些。
const (
	composeRate    = 16000
	composeEdgeMs  = 60   // 切静音时每段首尾各留一点，免得吃掉轻声的字头字尾
	composeLeadMs  = 600  // 整条前后补静音：太短、结尾没静音的素材服务端拿不到 ASR final
	composeTailMs  = 1200 //
	composeFloor   = 600  // 静音判定：20ms 窗口峰值低于它（约 -35 dBFS）
	composeWindow  = composeRate / 50
	composeMaxPart = 16
)

func (s *Server) handleComposeAsset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AssetIDs []string `json:"asset_ids"`
		Name     string   `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if len(body.AssetIDs) < 2 || len(body.AssetIDs) > composeMaxPart {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("asset_ids 要 2～%d 条", composeMaxPart))
		return
	}
	// 同样的来源和顺序已经拼过就直接复用，simctl 反复 --compose 不会把素材库刷满。
	s.assetMu.Lock()
	for _, a := range s.assets {
		if slices.Equal(a.composedOf, body.AssetIDs) {
			resp := assetPublic(a)
			resp["reused"] = true
			s.assetMu.Unlock()
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	s.assetMu.Unlock()

	samples := make([]byte, 0, composeRate*2*10)
	samples = append(samples, make([]byte, composeRate*2*composeLeadMs/1000)...)
	var names, tags []string
	language := ""
	for i, id := range body.AssetIDs {
		p, _, code, msg := s.resolveAssetWAV(r.Context(), id, composeRate, 1)
		if code != 0 {
			writeErr(w, code, fmt.Sprintf("第 %d 条 %s：%s", i+1, id, msg))
			return
		}
		part := trimSilence(p.Samples)
		if len(part) == 0 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("第 %d 条 %s 整段是静音", i+1, id))
			return
		}
		samples = append(samples, part...)
		s.assetMu.Lock()
		if a := s.assets[id]; a != nil {
			names = append(names, a.name)
			for _, t := range a.tags {
				if !slices.Contains(tags, t) {
					tags = append(tags, t)
				}
			}
			if language == "" {
				language = a.language
			}
		}
		s.assetMu.Unlock()
	}
	samples = append(samples, make([]byte, composeRate*2*composeTailMs/1000)...)
	tags = append(tags, "组合")

	pcm := core.PCM{Samples: samples, SampleRate: composeRate, Channels: 1, BitsPerSample: 16}
	durMs := wavDurationMs(pcm)
	if maxDur := s.opts.Config.MaxAssetDurationSec; maxDur > 0 && durMs > maxDur*1000 {
		writeErr(w, http.StatusBadRequest, "拼完超过 max_asset_duration_sec")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = strings.Join(names, "+")
	}
	root := s.assetsRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := newAssetID()
	final := filepath.Join(root, id+".wav")
	data := core.EncodeWAV(pcm)
	if err := os.WriteFile(final, data, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	obj := &assetObj{
		id: id, path: final, epoch: 1, bytes: len(data), durationMs: durMs,
		sampleRate: composeRate, channels: 1, sampleFormat: "s16le",
		name: name, language: language, tags: tags, format: "wav", kind: assetKindAudio,
		bitrateKbps: composeRate * 16 / 1000, createdAt: time.Now().UnixMilli(),
		composedOf: slices.Clone(body.AssetIDs), variants: map[string]string{},
	}
	s.assetMu.Lock()
	s.assets[id] = obj
	_ = persistWarn("index.json", s.persistAssetIndexLocked())
	s.assetMu.Unlock()
	writeJSON(w, http.StatusCreated, assetPublic(obj))
}

// trimSilence 切掉 s16le 单声道 PCM 首尾的静音，首尾各留 composeEdgeMs。整段静音返回空。
// ponytail: 固定峰值门限，底噪很高的录音切不干净；真遇上再按整段噪声估门限。
func trimSilence(pcm []byte) []byte {
	win := composeWindow * 2 // 字节
	loud := func(off int) bool {
		end := min(off+win, len(pcm))
		for i := off; i+2 <= end; i += 2 {
			v := int16(binary.LittleEndian.Uint16(pcm[i:]))
			if v > composeFloor || v < -composeFloor {
				return true
			}
		}
		return false
	}
	first, last := -1, -1
	for off := 0; off < len(pcm); off += win {
		if loud(off) {
			if first < 0 {
				first = off
			}
			last = off + win
		}
	}
	if first < 0 {
		return nil
	}
	edge := composeRate * 2 * composeEdgeMs / 1000
	first = max(first-edge, 0)
	last = min(last+edge, len(pcm))
	last -= (last - first) % 2 // 保持样本对齐
	return pcm[first:last]
}
