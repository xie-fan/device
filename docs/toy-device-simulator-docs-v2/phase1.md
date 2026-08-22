# Phase 1 详细设计：单设备协议正确闭环（审查后修订版）

> 已按两份 plan-review 关闭所有 P1 阻塞项。

## 1. 目标

实现一个可独立运行的设备模拟器，完整跑通「握手 → 注册 → report → 按键模式发音频 → 接收 TTS」主路径，并保证本地校验错误与 expected_server_drop 可推断，与协议 + types.AudioHeader 对齐。

## 2. 范围与非目标

**范围**

- 独立 protocol 包（AudioHeader + 管理消息 + 无前缀 JSON）
- 三套正交状态机（Connection / UplinkTurn / DownlinkPlayer）
- Turn 基础抽象（含 uplink_end_reason / turn_end_reason）
- keepalive（周期 report）
- 本地校验 + expected_server_drop（inferred_no_reply）
- 文件切片上行 + UUID 对齐下行 + idle 结束
- 全量帧录制 + 音频落盘（路径含 device_id）+ 结构化日志
- YAML 单设备配置 + 校验
- **one-shot CLI**（唯一主命令）

**非目标**

- 批量设备
- Web UI
- Scenario / 断言引擎
- 完整连续模式流式发送
- 故障注入全集（仅支持注入以触发 expected_server_drop 的最小集）
- 独立多进程 start / interrupt / status
- JSON downlink-ack 与非零 SleepMs 模拟
- server_observed_drop（需服务端探针）

## 3. 推荐目录结构（Phase 1）

```text
toy-device-simulator/
├── protocol/
│   ├── header.go            # 或 .py，与选型一致
│   ├── message.go
│   └── header_test.go       # golden test
├── core/
│   ├── device.go
│   ├── turn.go
│   ├── events.go
│   ├── conn_sm.go           # 连接状态机
│   ├── uplink_sm.go         # 上行 Turn 状态机
│   └── downlink_sm.go       # 下行播放状态机
├── config/
├── recording/
├── cmd/
│   ├── speak/               # one-shot 主命令
│   └── check/               # 配置 + golden 自检
├── configs/
│   └── example_device.yaml
└── testdata/
    ├── golden_frames/       # 由 types.AudioHeader 生成
    └── audio/
```

## 4. protocol 包要求

- `AudioHeader` 固定 100 字节，小端，含 2 字节 padding。
- 必须提供 encode / decode，golden test 对照 `common/types/newProtocol.go` 的 `AudioHeader` 与协议文档手写/生成样例。
- **禁止**把 `example/asr/mock.go` 发出的帧（0x01 首字节等）收进 golden。
- 管理消息首字节必须是 ASCII `'1'`（0x31）；音频为 ASCII `'0'`（0x30）。
- 无前缀 JSON（`{` 开头）与带 `'1'` 的管理消息严格分流。
- 支持识别 `'4'` ACK。

**mock.go 已知偏差（实现时不要复制）：**

- 管理首字节使用 `0x01` 而非 `'1'`（0x31）
- 不发送 report
- Stage=2 发送被注释，主路径不发
- UUID 写死为 1
- 切片间隔注释与代码不一致

## 5. 正交状态机

### 5.1 连接状态机（Connection）

```text
Disconnected
  → Connecting（握手 Device/Action）
  → Connected
  → Registering
  → Registered（register/client 且 code==0）
  → Reporting
  → Ready

Ready / Connected 可因空闲超时或网络错误 → Disconnected
任意状态可产生 local_validation_error / protocol_error / ack_failure
```

### 5.2 上行 Turn 状态机（UplinkTurn）

仅在 Connection == Ready 后可启动。

```text
Idle
  → Speaking（Stage=1 分片中）
  → FinishingUpload（发 Stage=2）
  → WaitingReply

WaitingReply
  → Idle（匹配下行结束 或 timeout → expected_server_drop）

Speaking / WaitingReply
  → Interrupted（发 Stage=3 或 Stage=5）
  → Idle（强制换新 UUID，下一轮 Seq 必须从 0 开始）
```

### 5.3 下行播放状态机（DownlinkPlayer）

```text
Idle
  → PlayingTTS（收到匹配 uplink_uuid 的 Stage=1 分片）
  → Idle（idle 超时 / 新一轮 / 打断）
```

指令通道与上述三套状态机正交。读循环持续运行，永不因落盘或解码阻塞。

**关键硬规则**

- 新一轮必须从 Seq=0 + Stage=1 开始。
- 收到 Stage=4 → 立刻停发 Stage=1 并补 Stage=2。
- 下行以 idle 超时（默认 20s）或新一轮/打断结束，不依赖 Stage=2。
- 打断后必须换新 UUID，Seq 从 0 再开始。

## 6. Turn 数据结构（草案）

```yaml
turn:
  turn_id: "local-stable-id"
  device_id: "..."
  uplink_uuid: 123456
  start_ts: 1710000000.123
  end_ts: null
  uplink_end_reason: null      # stage2 | interrupt | vad | error | timeout
  turn_end_reason: null        # idle | interrupt | error | timeout
  uplink_chunks: []
  downlink_chunks: []
  tts_path: null
  audio_format: "wav"
  asr_results: []
  commands: []
  errors: []
  events: []
```

## 7. 配置 Schema（YAML 示例）

```yaml
device:
  enterprise: "demo"           # 须已在目标 core 配置
  device_type: "A3"            # 禁止 MH 前缀；须已有设备类型配置
  device_id: "sim_001"
  action: "chatbot"

  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"  # 新设备实质需要能过 iot.Check，或使用已有 status=1 设备
  location: ""

  playing_mode: 1              # 1=按键

  audio:
    format: "wav"
    sample_rate: 16000
    slice_ms: 100
    max_payload_size: 51200

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report" # 周期 report，避开 register 限流
    downlink_idle_timeout_sec: 20
    downlink_ack: false

  uuid:
    min: 1
    max: 2147483647            # 0x7FFFFFFF

  server:
    url: "ws://127.0.0.1:8089/"

  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings" # 实际路径 recordings/{device_id}/{turn_id}/
```

**最小联调前置（必须满足）：**

- 目标 core 中企业配置存在
- 设备类型配置存在
- 新设备 nic_iccid 能过 iot.Check，或直接使用已存在且 status=1 的设备
- 握手 Device 恰好三段，Action=chatbot
- 对齐基线的 commit 已填写

## 8. CLI 设计（one-shot only）

```bash
# 唯一主命令：单进程走完 握手→注册→report→切片上行→idle收口→落盘→退出
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

# 可选：只做配置校验 + golden 自检，不连网
go run ./cmd/check --config configs/example_device.yaml
```

不提供独立的 `start` / `interrupt` / `status` 多进程命令。长连接控制明确推迟到 Phase 2。

## 9. 帧录制与落盘

路径固定为：

```text
recordings/{device_id}/{turn_id}/
  ├── frames.jsonl
  ├── uplink.<ext>
  ├── downlink.<ext>
  └── turn.json
```

避免多设备或多 Turn 覆盖。

## 10. 验收 Checklist（修订后）

- [ ] 握手成功（Device 三段 + Action=chatbot）
- [ ] register 成功（code==0）并正确 report playingMode=1
- [ ] AudioHeader 100 字节与 golden 一致（权威源为 types.AudioHeader，非 mock.go）
- [ ] Seq 从 0 开始，Stage 1 → … → Stage 2 完整上行
- [ ] **真实下行至少收到一帧 `'0'` TTS，接收栈未因 UTF-8 解码失败**
- [ ] 按 UUID 对齐接收，idle 超时正确结束本轮，音频可落盘回放
- [ ] 全量帧日志可读
- [ ] 注入 skip_register / bad_seq / oversize 后，在 timeout 内出现 `expected_server_drop`（inferred_no_reply），并带对应 `injected_fault`
- [ ] 周期 keepalive（report）使连接空闲超过 360s 不掉线
- [ ] NeedAck=1 时回了二进制 `'4'` 且 SleepMs=0
- [ ] 不依赖 mock.go 主路径行为作为验收标准
