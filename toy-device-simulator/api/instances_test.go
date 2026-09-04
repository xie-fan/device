package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// trimLastLine 取 NDJSON 的最后一行（turn.json 是追加写的）。
func trimLastLine(raw []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{0x0a})
	return lines[len(lines)-1]
}

// runOneTurn 跑一整轮（含下行 TTS）并返回这次运行的 instance_id 与 turn_id。
// idle/first_reply 压到 1~2 秒，好让 TTS 之后立刻收口。
func runOneTurn(t *testing.T, e *testEnv, id string) (ins, turnID string) {
	t.Helper()
	body := e.deviceBody(id)
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	beh["first_reply_timeout_sec"] = 2
	if code, raw := e.post(t, "/devices", e.createBody(body)); code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, id)
	e.waitReady(t, id, ins, gen)
	assetID := e.uploadWAV(t)
	code, raw := e.post(t, "/devices/"+id+"/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 6,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait 应 200，得到 %d body=%s", code, raw)
	}
	turnID = strField(decodeMap(t, raw), "turn_id")
	if turnID == "" {
		t.Fatalf("终态应带 turn_id，body=%s", raw)
	}
	return ins, turnID
}

func listField(t *testing.T, body []byte, key string) []any {
	t.Helper()
	v, _ := decodeMap(t, body)[key].([]any)
	return v
}

// Phase 8 的正题：manager 重启后同一台设备换新 instance_id，旧 instance 的
// turn / 事件 / 帧 / 音频必须仍然取得到（此前一律 404 instance 未命中）。
func TestHistorySurvivesManagerRestart(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, turnID := runOneTurn(t, e, "sim_hist")

	e.srv.Close()
	e.start(t, 0)

	code, raw, _ := e.get(t, "/devices/sim_hist")
	if code != http.StatusOK {
		t.Fatalf("设备定义应跨重启存活，得到 %d %s", code, raw)
	}
	if newIns := strField(decodeMap(t, raw), "instance_id"); newIns == oldIns {
		t.Fatal("重启应换新 instance_id（世系模型不变）")
	}

	q := "?instance_id=" + oldIns
	code, raw, _ = e.get(t, "/devices/sim_hist/turns"+q)
	if code != http.StatusOK {
		t.Fatalf("旧 instance 的 /turns 应 200，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if src := strField(m, "source"); src != "disk" {
		t.Fatalf("重启后旧 instance 应来自盘，source=%q", src)
	}
	turns, _ := m["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("应读回 1 轮，得到 %d：%s", len(turns), raw)
	}
	row, _ := turns[0].(map[string]any)
	if strField(row, "turn_id") != turnID {
		t.Fatalf("turn_id 应为 %s，得到 %s", turnID, raw)
	}
	if strField(row, "turn_end_reason") == "" {
		t.Fatalf("终态原因应从 turn.json 读回，得到 %s", raw)
	}

	code, raw, _ = e.get(t, "/devices/sim_hist/turns/"+turnID+q)
	if code != http.StatusOK {
		t.Fatalf("旧 instance 的单轮查询应 200，得到 %d %s", code, raw)
	}

	code, raw, _ = e.get(t, "/devices/sim_hist/turns/"+turnID+"/frames"+q)
	if code != http.StatusOK || len(raw) == 0 {
		t.Fatalf("旧 instance 的帧日志应 200 且非空，得到 %d len=%d", code, len(raw))
	}

	code, raw, _ = e.get(t, "/devices/sim_hist/turns/"+turnID+"/audio/uplink"+q)
	if code != http.StatusOK || len(raw) == 0 {
		t.Fatalf("旧 instance 的上行音频应 200 且非空，得到 %d len=%d", code, len(raw))
	}
}

// 事件是唯一纯内存的那份，不落盘则「历史」只有半份。
func TestEventsSurviveManagerRestart(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, _ := runOneTurn(t, e, "sim_evh")

	code, raw, _ := e.get(t, "/devices/sim_evh/events?instance_id="+oldIns)
	if code != http.StatusOK {
		t.Fatalf("重启前 /events 应 200，得到 %d %s", code, raw)
	}
	before := listField(t, raw, "events")
	if len(before) == 0 {
		t.Fatal("重启前应已有事件")
	}

	e.srv.Close()
	e.start(t, 0)

	code, raw, _ = e.get(t, "/devices/sim_evh/events?instance_id="+oldIns)
	if code != http.StatusOK {
		t.Fatalf("重启后旧 instance 的 /events 应 200，得到 %d %s", code, raw)
	}
	after := listField(t, raw, "events")
	if len(after) != len(before) {
		t.Fatalf("盘上事件条数应与内存一致：重启前 %d，重启后 %d", len(before), len(after))
	}
	first, _ := after[0].(map[string]any)
	if strField(first, "instance_id") != oldIns || strField(first, "event_type") == "" {
		t.Fatalf("落盘事件应保持 GET /events 的形状，得到 %s", raw)
	}

	// 游标语义不变：after_event_seq 之后的才给。
	seq := intField(first, "event_seq")
	code, raw, _ = e.get(t, "/devices/sim_evh/events?instance_id="+oldIns+"&after_event_seq="+strconv.Itoa(seq))
	if code != http.StatusOK {
		t.Fatalf("带游标应 200，得到 %d %s", code, raw)
	}
	if got := len(listField(t, raw, "events")); got != len(after)-1 {
		t.Fatalf("游标应滤掉 seq<=%d 的，剩 %d 条（共 %d）", seq, got, len(after))
	}
}

// /wait 与 /ws/events 等的是将来的事件，盘上历史没有将来 —— 仍该 404。
func TestWaitRejectsDiskOnlyInstance(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, _ := runOneTurn(t, e, "sim_waitd")
	e.srv.Close()
	e.start(t, 0)

	code, raw := e.post(t, "/wait", map[string]any{
		"action": "wait", "device_id": "sim_waitd", "instance_id": oldIns,
		"event_type": "turn_terminal", "timeout_sec": 1,
	})
	if code != http.StatusNotFound {
		t.Fatalf("对盘上历史 /wait 应 404，得到 %d %s", code, raw)
	}
}

func TestListInstancesAcrossRestart(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, _ := runOneTurn(t, e, "sim_li")

	e.srv.Close()
	e.start(t, 0)
	code, raw, _ := e.get(t, "/devices/sim_li")
	if code != http.StatusOK {
		t.Fatalf("GET device %d %s", code, raw)
	}
	newIns := strField(decodeMap(t, raw), "instance_id")

	code, raw, _ = e.get(t, "/devices/sim_li/instances")
	if code != http.StatusOK {
		t.Fatalf("GET /instances 应 200，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if strField(m, "instance_id") != newIns {
		t.Fatalf("顶层 instance_id 应是本次运行，得到 %s", raw)
	}
	got := map[string]string{}
	for _, it := range listField(t, raw, "instances") {
		row, _ := it.(map[string]any)
		got[strField(row, "instance_id")] = strField(row, "source")
	}
	if got[oldIns] != "disk" {
		t.Fatalf("旧 instance 应列为 disk，得到 %v", got)
	}
	// 本次运行还没送过话，盘上没有目录，但必须出现在列表里。
	if got[newIns] != "live" {
		t.Fatalf("本次运行应列为 live，得到 %v", got)
	}
}

func TestDeleteInstanceRemovesRecordingsAndRefusesLive(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, _ := runOneTurn(t, e, "sim_di")
	e.srv.Close()
	e.start(t, 0)

	code, raw, _ := e.get(t, "/devices/sim_di")
	if code != http.StatusOK {
		t.Fatalf("GET device %d %s", code, raw)
	}
	liveIns := strField(decodeMap(t, raw), "instance_id")
	if code, raw := e.del(t, "/devices/sim_di/instances/"+liveIns); code != http.StatusConflict {
		t.Fatalf("删本次运行应 409，得到 %d %s", code, raw)
	}

	dir := filepath.Join(e.recDir, "sim_di", oldIns)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("删除前目录应在：%v", err)
	}
	if code, raw := e.del(t, "/devices/sim_di/instances/"+oldIns); code != http.StatusOK {
		t.Fatalf("删旧 instance 应 200，得到 %d %s", code, raw)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("目录应已删掉，err=%v", err)
	}
	code, raw, _ = e.get(t, "/devices/sim_di/turns?instance_id="+oldIns)
	if code != http.StatusNotFound {
		t.Fatalf("删后应 404，得到 %d %s", code, raw)
	}
}

// 上行回放原本靠「设备现在的配置」定格式；跨重启回看时那份配置可能已经改过，
// 所以格式记进 turn.json 并优先采信。改掉设备当前采样率后，旧录音的 WAV 头
// 必须仍是录这段时的 16000。
func TestUplinkFormatComesFromTurnJSONNotCurrentConfig(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	oldIns, turnID := runOneTurn(t, e, "sim_upfmt")

	// 重启顺带把 recorder 排干，turn.json 一定落到位。
	e.srv.Close()
	e.start(t, 0)

	code, raw, _ := e.get(t, "/devices/sim_upfmt/config")
	if code != http.StatusOK {
		t.Fatalf("GET config %d %s", code, raw)
	}
	audio, _ := decodeMap(t, raw)["audio"].(map[string]any)
	audio["sample_rate"] = 8000
	audio["max_payload_size"] = 25600
	if code, raw := e.put(t, "/devices/sim_upfmt/config", map[string]any{"audio": audio}); code != http.StatusOK {
		t.Fatalf("PUT config 应 200，得到 %d %s", code, raw)
	}

	code, wav, _ := e.get(t, "/devices/sim_upfmt/turns/"+turnID+"/audio/uplink?instance_id="+oldIns)
	if code != http.StatusOK || len(wav) < 28 {
		t.Fatalf("旧 instance 的上行应 200 且是 WAV，得到 %d len=%d", code, len(wav))
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("WAV 头应是录这段时的 16000（turn.json 记的），不是设备改后的 8000，得到 %d", got)
	}

	// 同一份记录也要能直接在盘上读到。
	rawTurn, err := os.ReadFile(filepath.Join(e.recDir, "sim_upfmt", oldIns, turnID, "turn.json"))
	if err != nil {
		t.Fatalf("读 turn.json：%v", err)
	}
	var row struct {
		UpFormat     string `json:"up_format"`
		UpSampleRate int    `json:"up_sample_rate"`
		UpChannels   int    `json:"up_channels"`
	}
	if err := json.Unmarshal(trimLastLine(rawTurn), &row); err != nil {
		t.Fatalf("解 turn.json：%v", err)
	}
	if row.UpFormat != "pcm" || row.UpSampleRate != 16000 || row.UpChannels != 1 {
		t.Fatalf("turn.json 应自描述上行格式，得到 %+v", row)
	}
}
