# Phase 1 详细设计：单设备协议正确闭环（v11）

本阶段做完必须能单独交付：**握手 → register → report → 按键 pcm 上行 → 收到 TTS（或矩阵内其它合法回复）→ CLI 退出/落盘**。  
正确性条款（timer 时序、缓冲、IsFinal）放在 §10.2，不替代 §10.1。

细则：`architecture.md`。下文重复实施本阶段所需契约，不要求翻其它版本。

## 1. 目标

可独立运行的 one-shot 模拟器，主路径 playMode=1。失败按 fault 矩阵可观测。周期 keepalive，避免 360s 空闲踢线。

## 2. 范围与非目标

**范围**

- protocol 包（AudioHeader、管理信封、无前缀 JSON、`'4'` ACK）
- Connection / UplinkTurn / DownlinkPlayer
- writePump；读循环不阻塞落盘
- keepalive（周期 report）+ `last_activity`
- pending_reports 锁内取号；register **先登记 timer 再发送**
- early_downlink_buf；完成矩阵
- fault 矩阵；pcm 上线；帧录制与音频落盘
- 完整 YAML；`cmd/speak` `cmd/check` `cmd/fixture`
- 音频与指令 binary ACK（SleepMs=0）

**非目标：** 批量 REST、UI、Scenario、JSON ACK、非零 SleepMs、queue、查 Mongo/DownlinkAck、改 device_id。

## 3. 目录

```text
toy-device-simulator/
├── protocol/
├── core/          # writePump, conn_sm, uplink, downlink, pending_reports, early_downlink, recording worker
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
```

**register（锁内先登记）：** `conn_mu` 下设 Registering、装 `register_ack_timeout` waiter，**然后** writePump 发送。任意 `/register/client` 锁内取消 timer。`code==0` → Registered；否则 `ack_failure` + 退出。无 ACK 到期退出。`skip_register` 不发送、不装 timer，Connection 停 Connected。

**report：** 锁内取号（首值=`report_sequence_start`）→ pending → writePump。匹配回显 → Ready。`skip_report` 停 Registered。

**Turn：** Ready 后 CAS Reserved（注入例外）。Stage=4 先 vad 再补 Stage=2。WaitingReply 前终态下行只缓存；回放只驱动计时。仅 IsFinal 可 silent。

**读循环：** 独立协程；指令立即进事件；落盘异步。

**writePump：** 全部出站；Stage=1/2 保序；写失败退出。

**keepalive：** Ready 后每 `keepalive_interval_sec`（默认 60）发 report；刷新 `last_activity`。

## 6. Turn

Reserved 时分配 turn_id / uplink_uuid。`uplink_end_reason` 先写不改。

## 7. 配置（完整 schema）

```yaml
device:
  enterprise: "demo"                 # 须已在目标 core 配置
  device_type: "A3"                  # 禁止 MH 前缀
  device_id: "sim_001"               # skip_register 时用夹具 ID 启动，不运行中改键
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1                    # 必须 ∈ {1,2,3}；Phase 1 主路径 1

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

`format` 非 pcm、`playing_mode` 非法、`mode: json` 或 `sleep_ms!=0` → 启动拒绝。`--wait` 用预算公式。

`--audio testdata/hello.wav`：解 RIFF 成内部 PCM 再分帧。

## 8. CLI

```bash
go run ./cmd/check --config configs/example_device.yaml
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
go run ./cmd/speak --config ... --wait --inject skip_register --device-id "$(夹具id)"
```

不查 Mongo。`--device-id` 仅为进程启动身份。

## 9. 录制

```text
recordings/{device_id}/{turn_id}/
  frames.jsonl  uplink.pcm  downlink.<ext>  turn.json
```

落盘不得阻塞读循环。

## 10. 验收

### 10.1 交付验收（本阶段通过标准）

- [ ] 握手 Device 三段 + `Action=chatbot`
- [ ] register `code==0`；report 回显后 Ready；上行携带配置的 `playingMode`
- [ ] pcm 分帧 Seq=0 起，Stage 1→2；真实收到至少一帧匹配 UUID 的 TTS（或矩阵内其它成功 reply_kind）
- [ ] `--wait` 在预算内结束；音频与 `turn.json` / `frames.jsonl` 落盘
- [ ] `cmd/speak` `cmd/check` `cmd/fixture` 可运行
- [ ] 周期 keepalive（report，默认 60s）；`last_activity` 可观测；空闲超过 360s **不掉线**
- [ ] 读循环在落盘进行时仍能处理下行指令
- [ ] 配置 `format: wav` 或非法 playing_mode 被拒绝
- [ ] skip_register 夹具 jsonl + drop；skip_report 不要求 drop

### 10.2 正确性附录（主路径已通后再勾）

- [ ] register：锁内登记 timer 后再发送；立即 ACK 不误杀；无 ACK / nack 退出
- [ ] 全部出站经 writePump；Stage=1/2 顺序；写失败退出
- [ ] report 首序号=start；并发取号无重复、无覆盖
- [ ] 长音频期间 command 不释放槽；回放不重复 ACK/事件/录帧
- [ ] 仅 IsFinal 可 silent；interim-only 为 timeout/drop
- [ ] binary ACK；Stage=4 vad 不被 Stage=2 覆盖
- [ ] 失败 JSON 在 Speaking 停上行
