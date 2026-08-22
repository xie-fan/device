# Phase 1 详细设计：单设备协议正确闭环（v17）

阶段边界不变。做完即交付：**握手 → register → report → pcm 上行 → 收到 TTS（或矩阵内其它合法回复）→ CLI 退出/落盘**。  
无 REST、无批量、无 JSON ACK、无 conn_permit、无 speak backlog。

本文件写全本阶段矩阵，不引用其它版本目录。同目录 `architecture.md` 含连接级细则。

## 1. 目标

one-shot CLI，主路径 playMode=1。失败按 fault 矩阵可观测。周期 keepalive，避免 360s 空闲踢线。每次 Turn 结束发 `turn_terminal`。进程退出前 wait `finalize_done`。

## 2. 范围与非目标

**范围：** AudioHeader / 管理信封 / 无前缀 JSON / `'4'` ACK；Connection / UplinkTurn / DownlinkPlayer；outbound buffer + BeginClose 清队列；读循环不阻塞落盘；keepalive；pending_reports 解锁后再 enqueue；register 先登记 timer 再发送且一次性消费；early_downlink_buf；完成矩阵（§6）；取消表（§6）；fault（§6）；pcm；完整 YAML；`cmd/speak` `cmd/check` `cmd/fixture`；binary ACK SleepMs=0；三段收口。

**非目标：** REST、UI、Scenario、JSON ACK、非零 SleepMs、speak backlog、改 device_id。

## 3. 目录

```text
toy-device-simulator/
├── protocol/
├── core/
├── cmd/speak/  cmd/check/  cmd/fixture/
├── configs/example_device.yaml
└── testdata/golden_frames/  testdata/fixtures/  testdata/inbound/  testdata/audio/
```

## 4. protocol

100 字节头，golden 对照基线 `AudioHeader`。禁止 mock.go。识别 `'0'` `'1'` `'4'` `'{'`。

## 5. 状态机

```text
Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready
收口：Disconnecting → BeginClose → closer drain/close → Disconnected
```

锁顺序：`device_mu` → `conn_mu` → `report_mu`。泵协程不回锁。closer 不得是读循环自己。

register：锁内 Registering + attempt + timer + settle，解锁后 Enqueue。ACK∥timeout `try_consume`（只能消费一次）；失败解锁后 async 收口。`Timer.Stop` 不能代替消费。skip_register 不发送、不装 timer，停 Connected。

report：`report_mu` 内取号、递增、登记 pending，**解锁后再 Enqueue**。首序号=`report_sequence_start`。skip_report 停 Registered。

Turn：先把 `--audio` WAV 读入内存再 CAS。Stage=4 先 vad。WaitingReply 前只缓存。`--wait` 预算：`upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。

BeginClose（泵协程不回锁）：`closing=true`；优雅关闭时删除本 Turn 未写出的 Stage 1/2、清空其余帧、**追加 Stage=3 作为最后一帧**（未发 Stage=2 不得因 FIFO 发出）。异常 / Reserved / 已 Terminal：清空 buffer，无 Stage=3。已从队列取出正在 Write 的帧视为已发出。closer 协程跑 drain/close，不得是读循环自己。

## 6. 完成矩阵、取消表、fault（全文）

**关联**

| 种类 | 形态 | 判定 |
|------|------|------|
| TTS | `'0'` | UUID == uplink_uuid |
| command | `'1'` `/command/client` | 已 Speaking 或之后 |
| asr_result | Action=asr_result | SessionID；完成只认 IsFinal |
| 成功 JSON | Code==0 非 asr_result | 同 command |
| 失败 JSON | Code=1 / 14007 | 立即停上行 |

**计时（仅 WaitingReply）**

| 计时器 | 规则 |
|--------|------|
| first_reply | 进入 WaitingReply；终态取消；默认 20s |
| TTS idle | 匹配 TTS 重置 20s |
| followup | command/JSON 无 TTS；5s；其后 TTS 改 idle |
| post_final_asr_silence | first_reply 到期且 IsFinal 且无终态；interim-only 禁止 |

**终止（均 `turn_terminal`）**

| 路径 | reply_kind | turn_end_reason | 额外 |
|------|------------|-----------------|------|
| 仅 TTS idle | tts | idle | tts_done |
| 仅 command | command | idle | 无 tts_done |
| 仅成功 JSON | json | idle | 无 tts_done |
| command+TTS | command+tts | idle | tts_done |
| JSON+TTS | json+tts | idle | tts_done |
| 仅 IsFinal 无终态 | silent | idle | 无 tts_done |
| 仅 interim 或全无（正常） | 空 | timeout | 无 expected_server_drop |
| 同上且 fault 为 drop 行 | 空 | timeout | expected_server_drop |
| 失败 JSON | 空 | error | protocol_error |
| interrupt | 保持或空 | interrupt | |
| 连接收口 | 保持或空 | connection_lost | |

WaitingReply 前不 Terminal（失败 JSON 除外）。回放禁止二次 ACK/事件/录帧。`early_downlink_buf` 容量 32。`finalize_started` 后本表不 Terminal。

**取消表**

| 状态 | 出站 | 空 uplink_end_reason |
|------|------|----------------------|
| Reserved | 不发 Stage=3 | 保持空 |
| Speaking | Stage=3；停 Stage=1 | interrupt / error |
| FinishingUpload Stage=2 未发 | 不发 Stage=2；Stage=3 | interrupt / error |
| FinishingUpload Stage=2 已发 | Stage=3 | 保持 |
| WaitingReply | Stage=3 | 保持 |
| Terminal | 无 | 不变 |

**fault**

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report | 不默认 drop |
| bad_seq | CAS 后 Seq>=1 | drop |
| oversize | payload>51200 | drop |
| bad_header | `'0'` + 少于 100 字节 | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

夹具 `sim_sr_{run_uuid}_{n}` 写入 `testdata/fixtures/fresh_ids.jsonl`。

## 7. 配置

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

`format` 非 pcm、非法 playing_mode、json ACK 或 sleep_ms≠0 → 启动拒绝。

## 8. CLI

```bash
go run ./cmd/check --config configs/example_device.yaml
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
go run ./cmd/speak --config ... --wait --inject skip_register --device-id "$(夹具id)"
```

## 9. 录制

```text
recordings/{device_id}/{turn_id}/frames.jsonl
recordings/{device_id}/{turn_id}/uplink.pcm
recordings/{device_id}/{turn_id}/downlink.pcm
recordings/{device_id}/{turn_id}/turn.json
```

落盘不阻塞读循环。

## 10. 验收

### 10.1 交付

握手 Device 三段 + chatbot；register code=0；report Ready；pcm Seq=0 Stage 1→2；收到匹配 TTS 或矩阵内其它成功 reply_kind；`--wait` 预算内结束（含加长 post_final_asr_silence 的 silent）；落盘；三命令可运行；keepalive 360s 不掉线；读循环不堵；wav format 拒绝；skip_register jsonl+drop；skip_report 不要求 drop；退出 Disconnected。

### 10.2 正确性

ACK∥timeout 一次消费；BeginClose 丢未发 Stage 1/2 再发 Stage 3；解码失败不占槽；全部终态有 `turn_terminal` 且字段符合 §6 终止表。
