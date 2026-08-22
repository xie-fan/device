# Phase 1 详细设计：单设备协议正确闭环（v5）

矩阵、占用、pcm 管线见 `architecture.md`。

## 1. 目标

握手 → register ACK → report 回显（按 sequence_number）→ pcm 上行 → 收 TTS。注入按矩阵。one-shot CLI。

## 2. 范围与非目标

**范围：** protocol、Connection + UplinkTurn（含 Reserved）、pcm 分帧、夹具新鲜 ID、fault 矩阵、帧录制、`cmd/speak` / `cmd/check`。

**非目标：** 批量、UI、Scenario、连续时间轴、JSON ACK、非零 SleepMs、queue、线上 wav/mp3、CLI 查 Mongo。

## 3. 目录

`protocol/` `core/` `cmd/speak` `cmd/check` `testdata/fixtures/` `testdata/audio/`（源文件可为 wav，上线为 pcm）。

## 4. protocol

100 字节头；`'0'`/`'1'`/`'4'`/`'{'`；golden 对照基线；禁止 mock.go 帧。

## 5. 状态机

### 5.1 Connection

```text
Disconnected → Connecting → Connected
  → Registering → Registered     # /register/client 且 code==0
  → Reporting → Ready            # /report/client 且 sequence_number 匹配本次上行
```

- 回显必须同时：topic 以 `/report/client` 结尾；`data` 为 `ReportData`；**`data.sequence_number` == 刚发出的 report.sequence_number**。只匹配 topic/`playingMode` 不够（会误收 keepalive 延迟回显）。
- 不是 AckResponse，不等 `data.code`。
- register `code!=0` → `ack_failure`。
- skip_register：Connected 停住。skip_report：Registered 停住。

### 5.2 UplinkTurn

正常路径：仅 Ready 后 **CAS Reserved**。  
注入：skip_register 在 Connected 上 CAS；skip_report 在 Registered 上 CAS。

```text
（槽空）--CAS--> Reserved → Speaking → FinishingUpload → WaitingReply → Terminal
Speaking + Stage=4 → FinishingUpload
```

Phase 1 one-shot 无二次 speak；状态机仍实现 Reserved 与取消表（供单测与 Phase 2）。取消表见架构 §4.5。

### 5.3 DownlinkPlayer

Idle → PlayingTTS → Idle。`NeedAck=1` 回 `'4'` SleepMs=0。

## 6. Turn

Reserved 时写入 `turn_id`、`uplink_uuid`。`uplink_end_reason=stage2` 后禁止改写。

## 7. 配置

```yaml
device:
  enterprise: "demo"
  device_type: "A3"
  device_id: "sim_001"          # 正常路径；skip_register 时由夹具覆盖
  action: "chatbot"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1

  audio:
    format: "pcm"               # Phase 1/2 线上唯一合法值
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
    downlink_idle_timeout_sec: 20
    expect_downlink_need_ack: false

  uuid:
    min: 1
    max: 2147483647

  server:
    url: "ws://127.0.0.1:8089/"
```

线上 `format` 非 `pcm` → `local_validation_error`，不连网。  
源 `--audio testdata/hello.wav`：解 RIFF 成内部 PCM 再分帧；帧 payload 为 pcm 字节，**无 RIFF**。

## 8. CLI 与夹具

```bash
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

# skip_register：ID 来自夹具，不由 CLI 查库
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait \
  --inject skip_register --device-id "$(fixture 本次分配的 id)"

go run ./cmd/speak --config ... --inject skip_report --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject bad_seq --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject oversize --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject bad_header --wait --audio testdata/hello.wav

go run ./cmd/check --config configs/example_device.yaml
```

- speak **不**实现「若 Mongo 已有 status=1 则拒绝」。
- 夹具约定：隔离 core + `sim_sr_{run_uuid}_{n}`；本趟 setup 不 register 该 ID。证明写在 `fresh_ids.jsonl` 与套件 README，不写在 speak 里。
- `bad_header`：`'0'` + 10 字节 `0x00`。

## 9. 录制

`frames.jsonl` 可核对 pcm payload 无 RIFF magic（`52 49 46 46`）出现在上行音频帧。

## 10. 验收

**正常路径**

- [ ] 握手；register `code==0`
- [ ] Ready 仅在 `sequence_number` 匹配的 report 回显之后
- [ ] 头 100 字节 golden；Seq 0→Stage2；payload 为 pcm
- [ ] 至少一帧 TTS；`tts_done`；`turn_end_reason=idle`
- [ ] speak 开始时已有 `turn_id`（Reserved），不是第一帧之后才有
- [ ] NeedAck=1 → `'4'` SleepMs=0
- [ ] 配置 `format: wav` 被拒绝

**注入**

- [ ] skip_register 使用夹具 ID：drop；不要求 speak 查库
- [ ] skip_report：不要求 drop
- [ ] bad_seq / oversize / bad_header：按矩阵 drop
- [ ] 并发两次 speak（可用单测假时钟）：一次 Reserved，一次失败，无双发
