package api

// Phase 8 跨重启的历史。
//
// instance_id 是每进程新生成的（POST /devices 分配一次，stop/start 只换
// conn_generation），而录音与事件按 instance 分区。设备定义落盘之后，manager 重启会给
// 同一台设备换一个新 instance_id——旧目录还躺在盘上，API 却一律 404 instance 未命中。
//
// 这里走的是 phase7.md §5 两条被否掉的修法之外的第三条：**盘就是历史的真相源**。
// 世系模型一个字不动（instance_id 不复用，两次运行的事件游标不会串），tombstone 的
// TTL 语义也不动；只是 instance 的解析多认一种来源——盘上有目录就能读。
//
// 代价是事件必须落盘：turn 元数据、帧日志、音频本来就在盘上，只有事件日志是纯内存的
// ring buffer，不落盘的话「历史」只有半份。落盘走 EventLog 的 mirror 钩子接到
// Recorder 的异步队列上——mirror 在 EventLog 的临界区内被调，禁止同步 IO。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
	"toy-device-simulator/recording"
)

const eventsFileName = "events.jsonl"

// instView 把 live / tombstone / 盘上历史三种来源收成同一份只读视图，
// 五个历史端点据此不必各写一遍三分支。
//
// turns 是锁内拷贝的值而不是 *turnRec：视图要在 s.mu 之外用（读盘不能持锁），
// 而 live 设备会继续改自己那份 turnRec。
type instView struct {
	src    string // live | tomb | disk；空 = 哪儿都不认识
	outDir string
	audio  config.Audio
	turns  []turnRec
	log    *core.EventLog // live / tomb 才有；disk 的事件从 events.jsonl 读
	dir    string         // disk 才有：instance 目录
}

func (v instView) turn(turnID string) *turnRec {
	for i := range v.turns {
		if v.turns[i].TurnID == turnID {
			return &v.turns[i]
		}
	}
	return nil
}

// resolveInstance 自己管 s.mu：先在内存里找 live / tombstone，找不到再 stat 盘。
// 读盘一律在锁外。
func (s *Server) resolveInstance(deviceID, instanceID string) instView {
	s.mu.Lock()
	s.purgeExpiredTombsLocked()
	var v instView
	// 设备还在时，盘上历史的根目录与音频参数缺省跟随它的当前配置——
	// 老录音没记 up_format 时（Phase 8 之前）只能靠这个回落。
	if d, ok := s.devices[deviceID]; ok {
		v.outDir, v.audio = d.cfg.Recording.OutputDir, d.cfg.Audio
		if d.instanceID == instanceID {
			v.src, v.log = "live", d.log
			v.turns = copyTurns(d.turns)
		}
	}
	if v.src == "" {
		if t, ok := s.tombs[instanceID]; ok && t.deviceID == deviceID {
			v.src, v.log = "tomb", t.log
			v.outDir, v.audio = t.cfg.Recording.OutputDir, t.cfg.Audio
			v.turns = copyTurns(t.turns)
		}
	}
	if v.outDir == "" {
		v.outDir = s.opts.RecordingsDir
	}
	s.mu.Unlock()
	if v.src != "" {
		return v
	}
	return s.diskInstance(v, deviceID, instanceID)
}

// diskInstance 盘上历史：目录在就认，turn 元数据从各 turn.json 读回。
func (s *Server) diskInstance(v instView, deviceID, instanceID string) instView {
	dir, err := core.RecordingInstanceDir(v.outDir, deviceID, instanceID)
	if err != nil {
		return instView{}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return instView{}
	}
	v.src, v.dir = "disk", dir
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		row, ok := readTurnRow(filepath.Join(dir, e.Name(), "turn.json"))
		if !ok {
			// turn.json 还没落（进程被杀在半路）：目录名就是 turn_id，
			// 帧日志与音频照样能取，只是没有终态。
			row.TurnID, row.InstanceID = e.Name(), instanceID
		}
		v.turns = append(v.turns, turnRec{
			TurnID:       row.TurnID,
			InstanceID:   row.InstanceID,
			UplinkUUID:   row.UplinkUUID,
			SeqBefore:    row.SeqBefore,
			EndReason:    row.TurnEndReason,
			UplinkReason: row.UplinkEndReason,
			ReplyKind:    row.ReplyKind,
			SampleRate:   row.UpSampleRate,
			Channels:     row.UpChannels,
			UpFormat:     row.UpFormat,
			DownFormat:   row.DownFormat,
			DownBytes:    row.DownBytes,
		})
	}
	sort.Slice(v.turns, func(i, j int) bool { return v.turns[i].TurnID < v.turns[j].TurnID })
	return v
}

func copyTurns(m map[string]*turnRec) []turnRec {
	out := make([]turnRec, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TurnID < out[j].TurnID })
	return out
}

// readTurnRow 读 turn.json。文件是追加的 NDJSON，最后一行才是终态。
func readTurnRow(path string) (recording.TurnRow, bool) {
	var row recording.TurnRow
	raw, err := os.ReadFile(path)
	if err != nil {
		return row, false
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) == 0 {
		return row, false
	}
	if json.Unmarshal(lines[len(lines)-1], &row) != nil {
		return row, false
	}
	return row, true
}

// resolveLiveLocked 只认内存里的 live / tombstone，调用方须持 s.mu。
// /wait 与 /ws/events 用它而不是 resolveInstance：这两个等的是「将来的事件」，
// 盘上历史没有将来，对它们 404 才是对的。
func (s *Server) resolveLiveLocked(deviceID, instanceID string) (live *managedDevice, tomb *tombstone, found string) {
	s.purgeExpiredTombsLocked()
	if d, ok := s.devices[deviceID]; ok && d.instanceID == instanceID {
		return d, nil, "live"
	}
	if t, ok := s.tombs[instanceID]; ok && t.deviceID == deviceID && time.Now().Before(t.expires) {
		return nil, t, "tomb"
	}
	return nil, nil, ""
}

// ——— GET /devices/{id}/instances ———

// handleListInstances 列出这台设备在盘上留下的每一次运行。没有这个端点，重启后
// 谁也说不出旧 instance_id 叫什么，历史等于取不到。
func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	s.purgeExpiredTombsLocked()
	d, alive := s.devices[id]
	outDir, liveIns := s.opts.RecordingsDir, ""
	if alive {
		outDir, liveIns = d.cfg.Recording.OutputDir, d.instanceID
		if outDir == "" {
			outDir = s.opts.RecordingsDir
		}
	}
	tombIns := map[string]bool{}
	for ins, t := range s.tombs {
		if t.deviceID == id {
			tombIns[ins] = true
		}
	}
	s.mu.Unlock()

	dir, err := core.RecordingDeviceDir(outDir, id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ents, _ := os.ReadDir(dir)
	list := make([]map[string]any, 0, len(ents))
	seen := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		ins := e.Name()
		seen[ins] = true
		list = append(list, instanceSummary(filepath.Join(dir, ins), ins, srcOf(ins, liveIns, tombIns)))
	}
	// 本次运行还没送过话时盘上没有目录，但它必须出现在列表里——
	// 否则界面上「当前运行」这一项会凭空缺席。
	if liveIns != "" && !seen[liveIns] {
		list = append(list, map[string]any{"instance_id": liveIns, "source": "live", "turns": 0})
	}
	sort.Slice(list, func(i, j int) bool {
		a, _ := list[i]["started_at"].(string)
		b, _ := list[j]["started_at"].(string)
		if a != b {
			return a > b // 新的在前
		}
		return list[i]["instance_id"].(string) > list[j]["instance_id"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "instance_id": liveIns, "instances": list})
}

func srcOf(ins, live string, tombs map[string]bool) string {
	switch {
	case ins == live:
		return "live"
	case tombs[ins]:
		return "tomb"
	default:
		return "disk"
	}
}

// instanceSummary turn_id 是 turn_<UnixNano>，位数相同，字典序即时间序，
// 所以只读首尾两个 turn.json 就能给出时间范围，不必把整个 instance 翻一遍。
func instanceSummary(dir, ins, src string) map[string]any {
	ents, _ := os.ReadDir(dir)
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := map[string]any{"instance_id": ins, "source": src, "turns": len(names)}
	if len(names) == 0 {
		return out
	}
	if first, ok := readTurnRow(filepath.Join(dir, names[0], "turn.json")); ok {
		out["started_at"] = first.StartedAt
	}
	if last, ok := readTurnRow(filepath.Join(dir, names[len(names)-1], "turn.json")); ok {
		out["ended_at"] = last.EndedAt
	}
	return out
}

// handleDeleteInstance 删掉一次运行留在盘上的录音与事件。tombstone 到期会自动清，
// 但重启后 tombstone 就没了——历史现在看得见，就得有地方能删。
func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	id, ins := r.PathValue("id"), r.PathValue("instance_id")
	s.mu.Lock()
	outDir := s.opts.RecordingsDir
	if d, ok := s.devices[id]; ok {
		if d.instanceID == ins {
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "这是本次运行的 instance，不能删")
			return
		}
		if d.cfg.Recording.OutputDir != "" {
			outDir = d.cfg.Recording.OutputDir
		}
	}
	delete(s.tombs, ins)
	s.mu.Unlock()

	dir, err := core.RecordingInstanceDir(outDir, id, ins)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeErr(w, http.StatusNotFound, "instance 未命中")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "instance_id": ins, "deleted": true})
}

// ——— 事件落盘 ———

// eventSink 返回挂到 EventLog.SetMirror 上的镜像函数：全局总线 + events.jsonl。
// mirror 在 EventLog 的临界区里被调，只许拿叶子锁——Recorder.submit 拿的是自己的锁
// 且发送不阻塞（队列满即丢），符合这个约束。
func (s *Server) eventSink(deviceID, instanceID, outDir string, rec *recording.Recorder) func(core.Event) {
	if outDir == "" {
		outDir = s.opts.RecordingsDir
	}
	dir, err := core.RecordingInstanceDir(outDir, deviceID, instanceID)
	if err != nil {
		return s.bus.Publish
	}
	path := filepath.Join(dir, eventsFileName)
	return func(ev core.Event) {
		s.bus.Publish(ev)
		// 与 GET /events 用同一个 MarshalJSON，读回来无需再转换一次形状。
		if b, err := json.Marshal(ev); err == nil {
			rec.SubmitLine(path, append(b, '\n'))
		}
	}
}

// diskEvents 读回落盘的事件。盘上是全量追加，没有 ring buffer 的淘汰，
// 所以不存在游标过期，after 之后的一律给出。
func diskEvents(dir string, after int) []json.RawMessage {
	raw, err := os.ReadFile(filepath.Join(dir, eventsFileName))
	if err != nil {
		return []json.RawMessage{}
	}
	out := []json.RawMessage{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			EventSeq int `json:"event_seq"`
		}
		if json.Unmarshal(line, &head) != nil || head.EventSeq <= after {
			continue
		}
		out = append(out, append(json.RawMessage(nil), line...))
	}
	return out
}
