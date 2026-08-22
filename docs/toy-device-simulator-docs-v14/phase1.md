# Phase 1 详细设计：单设备协议正确闭环（v14）

本阶段边界不变：做完即交付 **握手 → register → report → pcm 上行 → 收到 TTS（或矩阵内其它合法回复）→ CLI 退出/落盘**。  
无 REST、无批量、无 JSON ACK、无 conn_permit。正确性条款在 §10.2，不替代 §10.1。

同目录 `architecture.md` 为完整契约。下文重复本阶段实施所需矩阵，不要求翻其它版本目录。

## 1. 目标

one-shot CLI，主路径 playMode=1。失败按 fault 矩阵可观测。周期 keepalive，避免 360s 空闲踢线。每次 Turn 结束发 `turn_terminal`。进程退出前 wait `finalize_done`。

## 2. 范围与非目标

**范围**

- protocol 包（AudioHeader、管理信封、无前缀 JSON、`'4'` ACK）
- Connection / UplinkTurn / DownlinkPlayer
- writePump + **BeginClose(finalFrame)**；读循环不阻塞落盘
- keepalive（周期 report）+ `last_activity`
- pending_reports：锁内取号，**解锁后再 enqueue**
- register：先登记 timer 再发送；一次性消费；**解锁后再 request_finalize**
- early_downlink_buf；完成矩阵（§6）；取消表（§6）
- `turn_terminal`；fault 矩阵（§6）
- pcm 上线；帧录制与音频落盘
- 完整 YAML；`cmd/speak` `cmd/check` `cmd/fixture`
- 音频与指令 binary ACK（SleepMs=0）
- 三段收口 + join-wait：Disconnecting → drain/close → Disconnected（无 permit）
- 等待预算含 `post_final_asr_silence`

**非目标：** 批量 REST、UI、Scenario、JSON ACK、非零 SleepMs、queue、conn_permit、`asset_id`、改 device_id。

## 3. 目录

```text
toy-device-simulator/
├── protocol/
├── core/          # writePump.BeginClose, request_finalize, uplink, downlink, pending_reports
├── config/
├── recording/
├── cmd/speak/  cmd/check/  cmd/fixture/
├── configs/example_device.yaml
└── testdata/golden_frames/  testdata/fixtures/  testdata/inbound/  testdata/audio/
```

## 4. protocol

100 字节头，golden 对照基线 `AudioHeader`。禁止 mock.go。识别 `'0'` `'1'` `'4'` `'{'`。

## 5. 状态机

```text
Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready
收口：任意状态 → Disconnecting →（BeginClose + 锁外 drain/close）→ Disconnected
```

**锁顺序：** `device_mu` → `conn_mu` → `report_mu`。持锁禁止 drain/close/wait `finalize_done`/调用 `request_finalize`。

**register：** 持 `conn_mu` 设 Registering、`register_attempt_id`、timer、`register_settle=pending`，**解锁后** Enqueue。ACK 与 timeout 均持锁做 `try_consume`；赢家若需退出则 **先解锁** 再 `request_finalize_async`。`code==0` → Registered。`code!=0` → `ack_failure` 再 nack 收口。`Timer.Stop` 不等待已开火 callback。`skip_register` 不发送、不装 timer，Connection 停 Connected。

**report：** `report_mu` 取号（首值=`report_sequence_start`）并登记 pending，**解锁**，再 Enqueue。匹配回显 → Ready。`skip_report` 停 Registered。

**Turn：** Ready 后 CAS Reserved（注入例外）。Stage=4 先 vad 再补 Stage=2。WaitingReply 前终态下行只缓存；回放只驱动计时。仅 IsFinal 可 silent。Terminal 必发 `turn_terminal` 再唤醒 `--wait`。每帧 Stage=1/2 入队前检查 `uplink_frozen` / Terminal / Disconnecting。

**读循环：** 独立协程；指令立即进事件；落盘异步。

**writePump：** 全部出站；深度见配置；Stage=1/2 保序。关闭：Phase A `BeginClose(finalFrame)`，Phase B drain。写失败走 `request_finalize_async(write)`。CLI 退出路径 **wait `finalize_done`**。

**keepalive：** Ready 后每 `keepalive_interval_sec`（默认 60）发 report；刷新 `last_activity`。

**`--wait` 预算：** `upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。

## 6. 完成矩阵、取消表、fault（本阶段正文）

关联、缓冲、计时、终止表、取消表、fault 帧定义与同目录 `architecture.md` §4.4 / §4.6 / §4.7 **相同**，实现必须按下列表，不得自行省略。

**关联**

| 种类 | 形态 | 本轮判定 |
|------|------|----------|
| TTS | `'0'` | UUID == uplink_uuid |
| command | `'1'` `/command/client` | 无 UUID；已 Speaking 或之后 |
| asr_result | `{` Action=asr_result | SessionID == uuid 十进制；记录 IsFinal |
| 成功 JSON | `{` Code==0，非 asr_result | 同 command |
| 失败 JSON | Code=1 / 14007 | 立即停上行 |

**计时（仅 WaitingReply 起）**

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态下行取消；默认 20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| 非音频 followup | command/成功 JSON 且尚无 TTS | 默认 5s；其后有 TTS 则改 idle |
| post_final_asr_silence | first_reply 到期且已有 IsFinal=true 且无终态下行 | 默认 5s → silent。interim-only 不得启动 |

**终止（均发 `turn_terminal`）**

| 路径 | `reply_kind` | `tts_done` |
|------|----------------|------------|
| 仅 TTS idle | tts | 有 |
| 仅 command/JSON followup | command / json | 无 |
| command 或 JSON 后 TTS | command+tts / json+tts | 有 |
| 仅 IsFinal 无终态 | silent | 无 |
| 仅 interim 或全无 | 空 | timeout / drop 行 `expected_server_drop` |
| 失败 JSON | 空 | `turn_end_reason=error` |

WaitingReply 前：不 Terminal（失败 JSON 除外）。回放只改计时器，禁止二次 ACK/事件/录帧/落盘。`early_downlink_buf` 容量 32。

**取消表与 BeginClose**

| 状态 | 优雅关闭 `finalFrame` | 空的 uplink_end_reason | turn_end_reason |
|------|----------------------|------------------------|-----------------|
| Reserved | 无 | 保持空 | interrupt / connection_lost |
| Speaking | Stage=3；停 Stage=1 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | Stage=3 | 保持 | 同上 |
| WaitingReply | Stage=3；取消计时器 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

Disconnecting/Disconnected：公开 Enqueue 全部拒绝。Stage=3 **只**由 `BeginClose` 注入。异常关闭 `finalFrame=无`，丢队列。Phase A 置 `uplink_frozen`，关队列前不得再入队 Stage 1/2。

**fault**

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1 | drop（服务端无 ASR session） |
| oversize | payload>51200 | drop |
| bad_header | `'0'` + 其后少于 100 字节 | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

skip_register 夹具 `sim_sr_{run_uuid}_{n}`，本趟不预注册，写入 `testdata/fixtures/fresh_ids.jsonl`。

## 7. 配置（完整 schema）

Phase 1 无 Manager，队列参数属于本 YAML。

```yaml
device:
  enterprise: "demo"                 # 须已在目标 core 配置
  device_type: "A3"                  # 禁止 MH 前缀
  device_id: "sim_001"               # skip_register 时用夹具 ID 启动
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1                    # 必须 ∈ {1,2,3}；主路径 1

  audio:
    format: "pcm"                    # Phase 1/2 线上唯一合法值
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
    downlink_ack:
      mode: binary
      sleep_ms: 0
      code: 0

  uuid:
    min: 1
    max: 2147483647

  server:
    url: "ws://127.0.0.1:8089/"

  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings"
```

`format` 非 pcm、`playing_mode` 非法、`mode: json` 或 `sleep_ms!=0` → 启动拒绝。`--audio testdata/hello.wav`：解 RIFF 成内部 PCM 再分帧（仅 CLI 本地路径）。

## 8. CLI

```bash
go run ./cmd/check --config configs/example_device.yaml
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
go run ./cmd/speak --config ... --wait --inject skip_register --device-id "$(夹具id)"
```

不查 Mongo。`--device-id` 仅为进程启动身份。`--wait` 等 `turn_terminal`，预算见 §5。

## 9. 录制

```text
recordings/{device_id}/{turn_id}/
  frames.jsonl  uplink.pcm  downlink.<ext>  turn.json
```

落盘不得阻塞读循环。Phase 2 HTTP 下载时再包成 `audio/wav`。

## 10. 验收

### 10.1 交付验收

- [ ] 握手 Device 三段 + `Action=chatbot`
- [ ] register `code==0`；report 回显后 Ready；上行携带配置的 `playingMode`
- [ ] pcm 分帧 Seq=0 起，Stage 1→2；真实收到至少一帧匹配 UUID 的 TTS（或矩阵内其它成功 reply_kind）
- [ ] `--wait` 在预算内结束（含加长 `post_final_asr_silence_sec` 的 silent 路径）
- [ ] 音频与 `turn.json` / `frames.jsonl` 落盘
- [ ] `cmd/speak` `cmd/check` `cmd/fixture` 可运行
- [ ] 周期 keepalive（report，默认 60s）；`last_activity` 可观测；空闲超过 360s **不掉线**
- [ ] 读循环在落盘进行时仍能处理下行指令
- [ ] 配置 `format: wav` 或非法 playing_mode 被拒绝
- [ ] skip_register 夹具 jsonl + drop；skip_report 不要求 drop
- [ ] 进程退出后 Connection=Disconnected（已 wait `finalize_done`）

### 10.2 正确性附录

- [ ] register：锁内登记后再发送；立即 ACK 不误杀；ACK∥timeout 只消费一次；nack/timeout **解锁后**再收口
- [ ] 持 `conn_mu` 时不 drain/close；后来者 join 同一次收口
- [ ] `BeginClose` 后不再入队 Stage 1/2；优雅关闭 Stage=3 只经 BeginClose
- [ ] 全部出站经 writePump；`report_mu` 不跨 Enqueue
- [ ] 长音频期间 command 不释放槽；回放不重复 ACK/事件/录帧
- [ ] 仅 IsFinal 可 silent；interim-only 为 timeout/drop
- [ ] 所有终态都有 `turn_terminal`
- [ ] binary ACK；Stage=4 vad 不被 Stage=2 覆盖
- [ ] 失败 JSON 在 Speaking 停上行
