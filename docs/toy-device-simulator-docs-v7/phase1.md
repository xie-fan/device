# Phase 1 详细设计：单设备协议正确闭环（v7）

Turn 占用、先写不改、fault 矩阵、ACK 运行时规则见 `architecture.md`。

## 1. 目标

跑通：握手 → register ACK → report 回显（原子序号）→ pcm 上行 → 接收 TTS。  
注入只按矩阵验收。one-shot CLI。

## 2. 范围与非目标

**范围**

- 独立 protocol 包（AudioHeader、管理信封、无前缀 JSON、`'4'` ACK）
- Connection / UplinkTurn / DownlinkPlayer
- Turn（Reserved 时分配 id/uuid；`uplink_end_reason` / `turn_end_reason`）
- keepalive（周期 report）
- 正常路径本地校验 + 注入矩阵
- 内部 PCM 切片上线（format=pcm）
- 帧录制与音频落盘
- YAML 配置校验
- `cmd/speak`、`cmd/check`、`cmd/fixture`

**非目标**

- 批量设备、Web UI、Scenario 引擎
- 连续模式时间轴（Phase 2）
- JSON ACK、非零 SleepMs（Phase 2）
- queue、线上 wav/mp3
- CLI 查询 Mongo 或服务端 `DownlinkAck`

## 3. 目录结构

```text
toy-device-simulator/
├── protocol/
│   ├── header.go
│   ├── message.go
│   └── header_test.go
├── core/
│   ├── device.go
│   ├── turn.go
│   ├── events.go
│   ├── conn_sm.go
│   ├── uplink_sm.go
│   └── downlink_sm.go
├── config/
├── recording/
├── cmd/
│   ├── speak/
│   ├── check/
│   └── fixture/              # alloc-fresh-id；本阶段必须交付
├── configs/
│   └── example_device.yaml
└── testdata/
    ├── golden_frames/
    ├── fixtures/fresh_ids.jsonl
    └── audio/                # 源文件可为 wav，上线为 pcm
```

## 4. protocol 包

- `AudioHeader` 固定 100 字节，小端，含 2 字节 padding。
- 提供 encode / decode；golden 对照对齐基线 `common/types/newProtocol.go` 的 `AudioHeader` 与协议手写样例。
- **禁止**把 `example/asr/mock.go` 发出的帧收进 golden。
- 管理首字节 ASCII `'1'`（0x31）；音频 `'0'`（0x30）；识别 `'4'` 与 `'{'`。

**mock.go 已知偏差（不要复制）：**

- 管理首字节 `0x01` 而非 `'1'`
- 不发 report
- 主路径不发 Stage=2
- UUID 写死为 1
- 切片间隔注释与代码不一致

## 5. 正交状态机

### 5.1 Connection

```text
Disconnected
  → Connecting（握手 Header Device / Action=chatbot）
  → Connected
  → Registering
  → Registered          # '1' + topic 以 /register/client 结尾且 data.code==0
  → Reporting
  → Ready               # '1' + /report/client 且 sequence_number 匹配本次上行
```

- 不要把 `/report/client` 解析成 `AckResponse`，不要等 `data.code`。
- 回显必须同时满足：topic 五段且以 `/report/client` 结尾；`data` 为 `ReportData`；**`data.sequence_number` 等于刚发出的 report.sequence_number**。
- 所有上行 report（首次、keepalive）走 **同一原子递增器**。
- register `code != 0`（含 5001）→ `ack_failure`（事件带 `correlation_id`）。
- report 无匹配回显 → 超时，不是 `ack_failure`。
- 注入 `skip_register`：停在 Connected，不发 register。
- 注入 `skip_report`：Registered 后停住，不发 report。

本阶段事件：Ready 前用 `correlation_id`；CAS Reserved 之后用 `turn_id`。

### 5.2 UplinkTurn

- 正常路径：仅 `Connection == Ready` 后 CAS Reserved。
- 注入：`skip_register` 在 Connected 上 CAS；`skip_report` 在 Registered 上 CAS。

```text
（槽空）--CAS--> Reserved → Speaking → FinishingUpload → WaitingReply → Terminal

Speaking + 收到 Stage=4
  → 若 uplink_end_reason 为空则立即记 vad
  → FinishingUpload（停发 Stage=1，补 Stage=2）
```

Phase 1 one-shot 无第二次 speak；状态机仍实现 Reserved 与取消表（供单测与 Phase 2）。取消规则见 `architecture.md` §4.6。Reserved 取消：`uplink_end_reason` 保持空。

### 5.3 DownlinkPlayer

```text
Idle → PlayingTTS（匹配 uplink_uuid 的 Stage=1）→ Idle（idle / 新一轮 / 打断）
```

收到下行音频 **`NeedAck == 1`** → 发 `'4'` 二进制 ACK，SleepMs=0。  
`NeedAck == 0` → 不发 ACK。  
不读取服务端 `DownlinkAck` 配置。SleepMs=0 的缓存语义见 Phase 2（本阶段不测节流）。

指令通道与三套状态机正交。读循环不因落盘阻塞。

**硬规则：** 正常路径 Seq=0 + Stage=1；Stage=4 先记 vad 再补 Stage=2；下行不依赖 Stage=2。

## 6. Turn

Reserved 时写入 `turn_id`、`uplink_uuid`。  
`uplink_end_reason` 一旦非空（含 `vad`），后续 Stage=2 / 取消不得改写。

## 7. 配置

```yaml
device:
  enterprise: "demo"            # 须已在目标 core 配置
  device_type: "A3"             # 禁止 MH 前缀；须已有设备类型配置
  device_id: "sim_001"          # 正常路径；skip_register 时由夹具覆盖
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"   # 新设备须能过 iot.Check，或使用已有 status=1 设备
  playing_mode: 1

  audio:
    format: "pcm"               # Phase 1/2 线上唯一合法值
    sample_rate: 16000
    channels: 1
    sample_format: "s16le"
    slice_ms: 100
    max_payload_size: 51200     # 仅正常路径；oversize 注入不受此限

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report"
    report_sequence_start: 1    # 原子计数器初值，此后每次 report +1
    downlink_idle_timeout_sec: 20
    expect_downlink_need_ack: false   # 仅 fixture 注释，不是运行时开关
    downlink_ack:
      mode: binary
      sleep_ms: 0
      code: 0

  uuid:
    min: 1
    max: 2147483647            # 0x7FFFFFFF

  server:
    url: "ws://127.0.0.1:8089/"

  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings"
```

线上 `format` 不是 `pcm` → `local_validation_error`，不连网。  
`--audio testdata/hello.wav`：解 RIFF 成内部 PCM 再分帧；帧 payload 为 pcm 字节，无 RIFF。

**最小联调前置：** 对齐基线对应的 core 已启动；企业与设备类型已配置；正常路径设备 `status=1` 或新设备 ICCID 能过 IoT。

## 8. CLI 与夹具

```bash
go run ./cmd/check --config configs/example_device.yaml

go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl

go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

go run ./cmd/speak --config ... --audio testdata/hello.wav --wait \
  --inject skip_register --device-id "$(夹具分配的 id)"

go run ./cmd/speak --config ... --inject skip_report --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject bad_seq --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject oversize --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject bad_header --wait --audio testdata/hello.wav
```

- speak **不**查询 Mongo，**不**判断 `status=1`。
- 夹具：隔离 core；`id = sim_sr_{run_uuid}_{n}`；本趟 setup 不对该 id register；jsonl 含 `id, run_uuid, isolation, generated_at`。
- `skip_register` 验收必须读 jsonl，不能只看退出码。
- `bad_header` 出站：第一字节 `'0'`，其后恰好 10 字节 `0x00`。
- `bad_seq`：CAS Reserved 后发 Seq>=1，不断言「本地无 active」。

不提供独立多进程 `start` / `interrupt` / `status`。

## 9. 帧录制

```text
recordings/{device_id}/{turn_id}/
  ├── frames.jsonl
  ├── uplink.pcm
  ├── downlink.<ext>
  └── turn.json
```

`frames.jsonl` 可核对：pcm payload 无 RIFF magic（`52 49 46 46`）；`bad_header` 的头区长度 < 100。

## 10. 验收 Checklist

**正常路径**

- [ ] 握手 Device 三段 + `Action=chatbot`
- [ ] `/register/client` 且 `code==0`（事件带 `correlation_id`）
- [ ] Ready 仅在 `sequence_number` 匹配的 report 回显之后
- [ ] 首次 report 与 keepalive 序号递增且回显不串
- [ ] AudioHeader 100 字节与 golden 一致
- [ ] Seq 从 0，Stage 1→2；payload 为 pcm
- [ ] 真实至少一帧 `'0'` TTS，接收栈未因 UTF-8 失败；`tts_done`；`turn_end_reason=idle`
- [ ] speak 受理时已有 `turn_id`（Reserved）
- [ ] Ready 前事件带 `correlation_id`；speak 后带 `turn_id`
- [ ] `NeedAck==1` 才发 `'4'`（SleepMs=0）；`NeedAck==0` 不 ACK
- [ ] 代码路径无 `DownlinkAck` 配置查询
- [ ] 未注入超限 → `local_validation_error`，无对应 outbound
- [ ] 配置 `format: wav` 被拒绝
- [ ] 周期 report，空闲超过 360s 不掉线

**注入（逐行勾选）**

- [ ] `skip_register`：jsonl 证据 + 未预注册 + outbound 有音频 + `expected_server_drop`
- [ ] `skip_report`：outbound 有音频；**不**要求 drop；允许 TTS
- [ ] `bad_seq`：本地已 Reserved；Seq>=1；`expected_server_drop`
- [ ] `oversize`：payload>51200；`expected_server_drop`
- [ ] `bad_header`：头区 <100 字节；`expected_server_drop`
- [ ] 单测：两次并发 speak，一次 Reserved，一次失败，无双发
- [ ] Stage=4 后 `uplink_end_reason=vad`，补 Stage=2 后仍为 `vad`
- [ ] `cmd/fixture` 可运行并写入 jsonl
- [ ] 不以 mock.go 主路径为验收标准
