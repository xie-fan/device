# Phase 1 详细设计：单设备协议正确闭环（v4）

对齐基线与 fault 矩阵见 `architecture.md` §4.4、§10。

## 1. 目标

跑通握手 → 注册 ACK → report **回显** → 按键发音频 → 收 TTS。注入只按矩阵验收，不统一期待 drop。

## 2. 范围与非目标

**范围：** protocol 包、三套状态机、Turn、周期 report keepalive、正常路径校验、注入矩阵、内部 PCM 切片、帧录制、one-shot CLI（含 `--inject`）。

**非目标：** 批量、UI、Scenario、连续模式时间轴（Phase 2）、JSON ACK、非零 SleepMs、`server_observed_drop`、`dup_uuid`/`bad_stage` 的 drop 验收、`queue`。

## 3. 目录结构

与 v3 Phase 1 相同（`cmd/speak`、`cmd/check`、`protocol/`、`core/`）。

## 4. protocol 包

- 100 字节小端头 + 2 字节 padding 字段存在即可；golden 对照基线 struct，禁止 mock.go。
- `'1'` / `'0'` / `'4'` / `'{'` 分流。
- mock.go 偏差清单同 v3（0x01 首字节、不 report、不发 Stage=2 等）。

## 5. 正交状态机

### 5.1 Connection

```text
Disconnected → Connecting → Connected
  → Registering → Registered     # 仅当 /register/client 且 data.code==0
  → Reporting → Ready            # 仅当 /report/client 且 data 为 ReportData 回显

skip_register：停在 Connected，不发 register
skip_report：Registered 后停住，不发 report，不进入 Reporting
```

- **不要**把 `/report/client` 解析成 `AckResponse`，不要等 `data.code`。
- 回显匹配：topic 五段且以 `/report/client` 结尾；JSON `data` 含本次上行的 `playingMode`（及同结构字段）。收齐即 Ready。
- register `code != 0`（含 5001）→ `ack_failure`，保持 Registering 或按超时失败。
- report 无回显 → 超时，**不是** `ack_failure`。

### 5.2 UplinkTurn

- 正常路径：仅 Ready 后启动。
- 注入路径：按矩阵前置；`skip_register` 在 Connected 写音频；`skip_report` 在 Registered 写音频。

```text
Idle → Speaking → FinishingUpload → WaitingReply
Speaking + Stage=4 → FinishingUpload
WaitingReply → Turn terminal（tts_done+idle 或 timeout）
Speaking 时打断 → uplink_end_reason=interrupt，换 UUID
WaitingReply / PlayingTTS 时打断 → uplink_end_reason 保持 stage2（若已发 Stage=2），turn_end_reason=interrupt
```

Phase 1 one-shot 无第二次 speak；打断规则留给状态机与 Phase 2。

### 5.3 DownlinkPlayer

Idle → PlayingTTS（匹配 UUID）→ Idle（idle / 新一轮 / 打断）。

**硬规则：** 正常路径 Seq=0+Stage=1；Stage=4 补 Stage=2；下行不依赖 Stage=2；`NeedAck=1` 回 `'4'` SleepMs=0。

## 6. Turn 结构

同 `architecture.md` §4.1。`uplink_end_reason` 一旦因 Stage=2 赋值为 `stage2`，后续 interrupt 不得改写它。

## 7. 配置

```yaml
device:
  enterprise: "demo"            # 须已在 core 配置
  device_type: "A3"             # 禁止 MH 前缀
  device_id: "sim_001"          # 正常路径可用已有 status=1
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1

  audio:
    format: "wav"               # 线上封装；内部始终 PCM
    sample_rate: 16000
    channels: 1                 # 内部 PCM 声道，写死 mono
    sample_format: "s16le"      # signed 16-bit little-endian
    slice_ms: 100
    max_payload_size: 51200     # 仅正常路径；oversize 注入不受此限

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report"
    downlink_idle_timeout_sec: 20
    expect_downlink_need_ack: false   # 机型预期；NeedAck=1 仍必须 ACK

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

**内部 PCM（写死）：** 单声道、signed 16-bit LE、采样率 = `audio.sample_rate`。  
输入 WAV：解析 RIFF，取出 PCM；声道/位深/采样率不一致则转换或拒绝，**禁止**把多个 WAV 文件当字节拼接（会重复容器头）。

**最小联调前置：** 基线 core 已启动；企业与设备类型已配置；正常路径设备 `status=1` 或新设备 ICCID 能过 IoT。

## 8. CLI

```bash
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

# 注入；skip_register 必须另给确认不存在的 device_id
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject skip_register --device-id sim_new_never_seen
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject skip_report
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject bad_seq
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject oversize
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject bad_header

go run ./cmd/check --config configs/example_device.yaml
```

`--inject` 出站字节与期望 **以 `architecture.md` §4.4 为准**。  
`skip_register` 若 `device_id` 已在库且 `status=1`：CLI 必须拒绝启动（precondition），不得继续发音频去「等 drop」。

`bad_header` 出站：第一字节 `'0'`，其后严格少于 100 字节（推荐恰好 10 字节 `0x00`）。

## 9. 帧录制

`recordings/{device_id}/{turn_id}/frames.jsonl` 必须能看出注入 outbound 的实际长度（`bad_header` 的 payload 长度 < 100）。

## 10. 验收 Checklist

**正常路径**

- [ ] 握手 Device 三段 + `Action=chatbot`
- [ ] `/register/client` 且 `code==0`
- [ ] `/report/client` 为 `ReportData` 回显后进入 Ready（不解析 ACK）
- [ ] 头 100 字节与 golden 一致
- [ ] Seq 从 0，Stage 1→2；内部 PCM 切片（WAV 已解封装）
- [ ] 真实至少一帧 `'0'` TTS，无 UTF-8 失败；`tts_done`；`turn_end_reason=idle`
- [ ] 周期 report 保活；`NeedAck=1` 时 `'4'` SleepMs=0
- [ ] 未注入超限 → `local_validation_error`，无 outbound

**注入（逐行，禁止用一条「全部 drop」勾掉）**

- [ ] `skip_register` + 新 ID：outbound 有音频；`expected_server_drop`
- [ ] `skip_register` + 已有 `status=1` ID：启动失败 / `local_validation_error`，不发音频
- [ ] `skip_report`：outbound 有音频；**不**要求 drop；允许下行 TTS
- [ ] `bad_seq`：outbound Seq>=1；`expected_server_drop`
- [ ] `oversize`：outbound payload>51200；`expected_server_drop`
- [ ] `bad_header`：outbound 头区 <100 字节；`expected_server_drop`
- [ ] 无服务端日志原因名事件；不以 mock.go 为验收
