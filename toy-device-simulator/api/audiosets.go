package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// audioSet 音频集（Phase 13）：一组有序、有名字的音频资产，整组送话用——
// 上线验收、定期回归。内存、audio_sets.json、REST 同一个形状。
//
// 单独一个文件、不进 index.json：旧版 manager 改资产时会整份重写 index.json，
// 不认识的字段会被抹掉。
type audioSet struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	AssetIDs  []string `json:"asset_ids"` // 有序，允许重复；恒非 nil，JSON 里是 []
	CreatedAt int64    `json:"created_at"`
}

type audioSetsFile struct {
	AudioSets []audioSet `json:"audio_sets"`
}

func (s *Server) audioSetsPath() string {
	return filepath.Join(s.assetsRoot(), "audio_sets.json")
}

// loadAudioSets 在 loadAssetIndex 之后调（api.New，无并发）。素材库里已经没有的
// 资产从集里剔掉——删资产会被 409 挡住，走到这里只能是盘上文件被动过。
func (s *Server) loadAudioSets() {
	p := s.audioSetsPath()
	raw, err := os.ReadFile(p)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "audio sets: 读不了 %s: %v（本次启动没有音频集）\n", p, err)
		}
		return
	}
	var f audioSetsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		fmt.Fprintf(os.Stderr, "audio sets: %s 解析失败: %v（本次启动没有音频集）\n", p, err)
		return
	}
	for _, set := range f.AudioSets {
		kept := make([]string, 0, len(set.AssetIDs))
		for _, id := range set.AssetIDs {
			if _, ok := s.assets[id]; ok {
				kept = append(kept, id)
				continue
			}
			fmt.Fprintf(os.Stderr, "audio sets: %s 剔除 %s：素材库里没有\n", set.ID, id)
		}
		set.AssetIDs = kept
		s.audioSets = append(s.audioSets, set)
	}
}

// persistAudioSetsLocked 重写 audio_sets.json；调用方必须持 assetMu。
func (s *Server) persistAudioSetsLocked() error {
	b, err := json.MarshalIndent(audioSetsFile{AudioSets: s.audioSets}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.audioSetsPath(), b)
}

// audioSetRefsLocked 引用了这条资产的音频集名称。调用方持 assetMu。
func (s *Server) audioSetRefsLocked(assetID string) []string {
	var names []string
	for _, set := range s.audioSets {
		if slices.Contains(set.AssetIDs, assetID) {
			names = append(names, set.Name)
		}
	}
	return names
}

// checkAudioSetLocked 校验名称与条目；selfID 是 PUT 时自己的 id（改名不和自己撞）。
// 返回 0 表示通过。调用方持 assetMu。
func (s *Server) checkAudioSetLocked(selfID, name string, ids []string) (int, string) {
	if name == "" {
		return http.StatusBadRequest, "name 不能为空"
	}
	for _, set := range s.audioSets {
		if set.ID != selfID && set.Name == name {
			return http.StatusConflict, "已有同名音频集：" + name
		}
	}
	for _, id := range ids {
		a, ok := s.assets[id]
		if !ok {
			return http.StatusBadRequest, "素材库里没有 " + id
		}
		if a.kind == assetKindImage {
			return http.StatusBadRequest, id + " 是图片，不能进音频集"
		}
	}
	return 0, ""
}

// audioSetBody 用指针区分「没给」与「给了空值」：PUT 两项都必须给。
type audioSetBody struct {
	Name     *string   `json:"name"`
	AssetIDs *[]string `json:"asset_ids"`
}

func decodeAudioSetBody(r *http.Request) (audioSetBody, error) {
	var b audioSetBody
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("JSON 非法：%v", err)
	}
	return b, nil
}

func audioSetView(set audioSet) map[string]any {
	return map[string]any{
		"id": set.ID, "name": set.Name, "asset_ids": set.AssetIDs, "created_at": set.CreatedAt,
	}
}

func (s *Server) handleListAudioSets(w http.ResponseWriter, _ *http.Request) {
	s.assetMu.Lock()
	// PUT 换的是整个 AssetIDs 切片、不原地改，所以拷一份外壳就能在锁外编码。
	list := append([]audioSet{}, s.audioSets...)
	s.assetMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"audio_sets": list})
}

func (s *Server) handlePostAudioSet(w http.ResponseWriter, r *http.Request) {
	b, err := decodeAudioSetBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := ""
	if b.Name != nil {
		name = strings.TrimSpace(*b.Name)
	}
	ids := []string{}
	if b.AssetIDs != nil {
		ids = append(ids, *b.AssetIDs...)
	}
	s.assetMu.Lock()
	defer s.assetMu.Unlock()
	if code, msg := s.checkAudioSetLocked("", name, ids); code != 0 {
		writeErr(w, code, msg)
		return
	}
	set := audioSet{ID: "set_" + newAssetID()[4:], Name: name, AssetIDs: ids, CreatedAt: time.Now().UnixMilli()}
	s.audioSets = append(s.audioSets, set)
	err = persistWarn("audio_sets.json", s.persistAudioSetsLocked())
	writeJSON(w, http.StatusCreated, persistErr(audioSetView(set), err))
}

// handlePutAudioSet 整份替换：改名、增删、排序都走它。
func (s *Server) handlePutAudioSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := decodeAudioSetBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 两项都必须给：只想改名的请求不能顺手把条目清空。
	if b.Name == nil || b.AssetIDs == nil {
		writeErr(w, http.StatusBadRequest, "PUT 是整份替换，name 与 asset_ids 都要给（asset_ids 可以是 []）")
		return
	}
	name := strings.TrimSpace(*b.Name)
	ids := append([]string{}, *b.AssetIDs...)
	s.assetMu.Lock()
	defer s.assetMu.Unlock()
	i := slices.IndexFunc(s.audioSets, func(x audioSet) bool { return x.ID == id })
	if i < 0 {
		writeErr(w, http.StatusNotFound, "音频集不存在")
		return
	}
	if code, msg := s.checkAudioSetLocked(id, name, ids); code != 0 {
		writeErr(w, code, msg)
		return
	}
	s.audioSets[i].Name, s.audioSets[i].AssetIDs = name, ids
	err = persistWarn("audio_sets.json", s.persistAudioSetsLocked())
	writeJSON(w, http.StatusOK, persistErr(audioSetView(s.audioSets[i]), err))
}

// handleDeleteAudioSet 只删集，不动集里的音频。
func (s *Server) handleDeleteAudioSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.assetMu.Lock()
	defer s.assetMu.Unlock()
	i := slices.IndexFunc(s.audioSets, func(x audioSet) bool { return x.ID == id })
	if i < 0 {
		writeErr(w, http.StatusNotFound, "音频集不存在")
		return
	}
	s.audioSets = slices.Delete(s.audioSets, i, i+1)
	_ = persistWarn("audio_sets.json", s.persistAudioSetsLocked())
	w.WriteHeader(http.StatusNoContent)
}
