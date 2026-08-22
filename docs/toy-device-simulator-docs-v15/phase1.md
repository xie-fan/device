# Phase 1 详细设计：单设备协议正确闭环（v15）

本阶段边界不变：做完即交付 **握手 → register → report → pcm 上行 → 收到回复 → CLI/落盘**。  
无 REST、无批量、无 JSON ACK、无 conn_permit、无 speak backlog。正确性在 §10.2。

同目录 `architecture.md` 为完整契约。下文重复本阶段矩阵，不要求翻其它版本目录。

## 1. 目标

one-shot CLI，playMode=1。keepalive 防 360s 踢线。`turn_terminal`。退出前 wait `finalize_done`。

## 2. 范围与非目标

**范围：** protocol；三套状态机；**outbound buffer（writePump）** + BeginClose；读循环不阻塞；keepalive；pending_reports 解锁后再 enqueue；register 一次性消费；early_downlink；完成矩阵；`turn_terminal`；fault；pcm；YAML；`cmd/speak|check|fixture`；binary ACK；三段收口；预算含 `post_final_asr_silence`；`finalize_started` 后完成矩阵不 Terminal。

**非目标：** REST、UI、Scenario、JSON ACK、非零 SleepMs、**speak backlog**、conn_permit、`asset_id`、改 device_id。

## 3. 目录

`protocol/` `core/`（writePump.BeginClose、request_finalize）`cmd/speak|check|fixture/` `configs/example_device.yaml` `testdata/`

## 4–5. protocol 与状态机

100 字节头；禁止 mock.go。

```text
Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready
收口：Disconnecting → BeginClose + closer drain/close → Disconnected
```

锁顺序：`device_mu` → `conn_mu` → `report_mu`。持锁禁止 IO / wait finalize / 调 request_finalize。closer 不得是读循环自己。

register：锁内登记 timer 再 Enqueue；ACK∥timeout 一次性消费；失败解锁后 async 收口。skip_register 停 Connected。

report：取号解锁再 Enqueue。skip_report 停 Registered。

Turn：Ready 后 **先读本地 WAV 到内存再 CAS**（CLI 无并发 DELETE，仍遵守顺序以免占槽后解码失败）。Stage=4 先 vad。WaitingReply 前只缓存。`--wait` 预算含 `post_final_asr_silence`。

outbound buffer：深度见 YAML；关闭走 BeginClose。写出 Stage 3 前若已 Terminal 则撤销。

## 6. 完成矩阵、取消表、fault

**关联 / 计时 / 终止** 与 `architecture.md` §4.4 相同，表如下。

| 种类 | 判定 |
|------|------|
| TTS `'0'` | UUID == uplink_uuid |
| command | 已 Speaking 或之后 |
| asr_result | SessionID；完成只认 IsFinal |
| 成功 JSON | 同 command |
| 失败 JSON | 立即停上行 |

| 计时器 | 规则 |
|--------|------|
| first_reply | 进 WaitingReply；终态取消；20s |
| TTS idle | 匹配 TTS 重置 20s |
| followup | command/JSON 无 TTS；5s；其后 TTS 改 idle |
| post_final_asr_silence | first_reply 到期且 IsFinal 且无终态；interim-only 禁止 |

终止均 `turn_terminal`。仅 TTS 有 `tts_done`。`finalize_started` 后本表不把 Turn 置 Terminal；Phase C 用 `connection_lost`。

**取消表：** Reserved 无 Stage=3；Speaking/未发完 Stage=2 可 Stage=3；Terminal 无出站。Disconnecting 公开 Enqueue 拒绝；Stage=3 只经 BeginClose。

**fault：** skip_register → drop；skip_report 不默认 drop；bad_seq/oversize/bad_header → drop；dup_uuid/bad_stage 不作 drop 验收。夹具 `sim_sr_{run_uuid}_{n}` 写 `fresh_ids.jsonl`。

## 7. 配置

Phase 1 无 Manager，**outbound buffer** 参数属于本 YAML（`write_queue_depth` / `write_drain_timeout_sec` 仅指 writePump，不是 speak backlog）。

```yaml
device:
  enterprise: "demo"
  device_type: "A3"
  device_id: "sim_001"
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1
  audio:
    format: "pcm"
    sample_rate: 16000
    channels: 1
    sample_format: "s16le"
    slice_ms: 100
    max_payload_size: 51200
  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report"
    report_sequence_start: 1
    report_echo_timeout_sec: 5
    register_ack_timeout_sec: 5
    first_reply_timeout_sec: 20
    downlink_idle_timeout_sec: 20
    non_audio_followup_sec: 5
    post_final_asr_silence_sec: 5
    wait_timeout_slack_sec: 5
    write_queue_depth: 256
    write_drain_timeout_sec: 2
    expect_downlink_need_ack: false
    downlink_ack: { mode: binary, sleep_ms: 0, code: 0 }
  uuid: { min: 1, max: 2147483647 }
  server: { url: "ws://127.0.0.1:8089/" }
  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings"
```

非法 format / playing_mode / json ACK / 非零 SleepMs → 启动拒绝。

## 8–9. CLI 与录制

```bash
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
```

`--wait` 等 `turn_terminal`。落盘不阻塞读循环。

## 10. 验收

### 10.1 交付

握手→register code=0→report Ready→pcm 上行→合法回复；落盘；keepalive 360s 不掉线；读循环不堵；skip_register jsonl+drop；进程退出 Disconnected。

### 10.2 正确性

ACK∥timeout 一次消费；BeginClose 后无 Stage 1/2；迟到下行在收口中不先 Terminal 再发 Stage 3；解码失败不留下 Reserved；预算含加长 `post_final_asr_silence`；全部终态有 `turn_terminal`。
