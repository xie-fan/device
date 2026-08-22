# Phase 1 详细设计：单设备协议正确闭环（v9）

Turn 占用、完成矩阵、`pending_reports`、`fail_connection`、ACK 字段见 `architecture.md`。

## 1. 目标

跑通：握手 → register ACK → **初始 report 精确序号回显 → Ready** → pcm 上行 → 按 §4.4 结束 Turn（TTS / command / 成功 JSON / silent，而不是只等 TTS）。  
注入按矩阵。one-shot CLI。音频与指令 **binary ACK**（SleepMs=0）。

## 2. 范围与非目标

**范围**

- protocol 包；Connection / UplinkTurn / DownlinkPlayer
- Turn；`pending_reports`；完成矩阵与计时器
- 连接失败时进程退出前仍走清理（计时器、槽、pending）
- keepalive；fault 矩阵；pcm 上线；帧录制
- `cmd/speak` `cmd/check` `cmd/fixture`
- 音频与指令 binary ACK

**非目标**

- 批量、UI、Scenario 引擎、REST
- JSON ACK、非零 SleepMs、节流 A/B/C
- queue、线上 wav/mp3、查 Mongo/`DownlinkAck`
- 运行中改 `device_id`
- 静默成功与 drop 的服务端探针（Phase 4）

## 3. 目录结构

```text
toy-device-simulator/
├── protocol/   core/   config/   recording/
├── cmd/speak/  cmd/check/  cmd/fixture/
├── configs/example_device.yaml
└── testdata/golden_frames/  testdata/fixtures/  testdata/inbound/  testdata/audio/
```

`core` 含 `pending_reports.go`、`event_log.go`（`event_seq`）、完成矩阵计时。

## 4. protocol 包

- AudioHeader 100 字节；golden 对照基线；禁止 mock.go。
- 识别 `'1'` `'0'` `'4'` `'{'`。
- 编码 binary ACK（架构 §4.9）。
- 解析无前缀 JSON：`Action=asr_result`、`Code`、`chat_reply`。

**mock.go 偏差（不要复制）：** 管理首字节 `0x01`；不 report；主路径不发 Stage=2；UUID 写死 1。

## 5. 状态机

### 5.1 Connection 与 pending_reports

```text
Disconnected → Connecting → Connected → Registering → Registered
  → Reporting → Ready
```

- report 匹配见架构 §4.8。**首个序号 = `report_sequence_start`（默认 1）**。
- initial 超时或发送失败 → `fail_connection`，不是 `ack_failure`。
- `skip_register`：停 Connected。`skip_report`：停 Registered。
- keepalive 仅 Ready 之后。

**乱序测试：** 回显 seq=2 先于 seq=1；initial 为 1 时不得因 2 先到而 Ready；`correlation_id` 不交叉。

握手/读写失败：按架构 §4.11 清理后进程非零退出；`--wait` 不得悬挂。

### 5.2 UplinkTurn

正常路径仅 Ready 后 CAS。注入：`skip_register` 在 Connected；`skip_report` 在 Registered。

```text
Reserved → Speaking → FinishingUpload → WaitingReply → Terminal
```

Stage=4：先记 `vad`（若空）再补 Stage=2。进入 WaitingReply 后按 §4.4 启动 `first_reply_timer`。

### 5.3 下行与 ACK

读循环并行处理：匹配 TTS、command、asr_result、成功/失败 JSON。

- 音频 `NeedAck==1` → `'4'` binary，SleepMs=0。
- 指令 `need_ack==1` → `'4'`，DownlinkType=3。
- 标志 0 不 ACK。
- 联调若无真实指令：注入 `testdata/inbound/` 仍须验收 ACK 编码。

`--wait` 等到 Terminal；drop 由首包超时收口；纯 command / 文本 JSON 由 followup 收口，**不得**再空等 20s idle。

## 6. Turn

Reserved 时分配 `turn_id` / `uplink_uuid`。`uplink_end_reason` 先写不改。`reply_kind` 按矩阵填写。

## 7. 配置

```yaml
device:
  enterprise: "demo"
  device_type: "A3"             # 禁止 MH 前缀
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
    report_sequence_start: 1    # 第一个发出的序号就是 1
    report_echo_timeout_sec: 5
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

`format` 非 pcm、`mode: json`、`sleep_ms != 0` → 启动拒绝。

`--wait` 默认超时 = 架构预算公式，禁止固定 30s。

## 8. CLI

```bash
go run ./cmd/check --config configs/example_device.yaml
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
go run ./cmd/speak --config ... --wait --inject skip_register --device-id "$(夹具id)"
```

`--device-id` 仅为启动身份。speak 不查 Mongo。`skip_register` 验收读 jsonl。

## 9. 帧录制

```text
recordings/{device_id}/{turn_id}/frames.jsonl  uplink.pcm  downlink.<ext>  turn.json
```

`turn.json` 含 `reply_kind`、`turn_end_reason`、`uplink_end_reason`。

## 10. 验收 Checklist

- [ ] register / initial 序号 = start / Ready 仅 initial 命中
- [ ] 乱序回显；无 latest；无 start+1 错位
- [ ] 对话 TTS：`tts_done` + idle
- [ ] 纯 command：followup 内 Terminal，`reply_kind=command`，无 `tts_done`
- [ ] 成功 JSON：`json_reply` + idle，无强制 `tts_done`
- [ ] command+TTS：先 command 后 TTS 时切到 idle，两者都记
- [ ] 匹配 asr_result 后无终态：silent idle，不是 20s TTS idle
- [ ] drop：无相关下行 → `expected_server_drop`；`--wait` 不悬挂
- [ ] 音频/指令 NeedAck 标志驱动 binary ACK
- [ ] 握手或读写失败：清理槽与 pending，非零退出
- [ ] 配置 wav / json ACK / 非零 SleepMs 拒绝
- [ ] skip_register jsonl；skip_report 不要求 drop
- [ ] bad_seq / oversize / bad_header 按矩阵
- [ ] Stage=4 后 vad 不被 Stage=2 覆盖
- [ ] 不以 mock.go 为验收标准
