# Phase 1 详细设计：单设备协议正确闭环（v3）

> 吸收 v2 审查。规划层 P1 已关闭。对齐基线见 `architecture.md` §10。

## 1. 目标

实现可独立运行的设备模拟器，跑通「握手 → 注册 → report → 按键模式发音频 → 接收 TTS」，并支持注入通道上的 `expected_server_drop` 推断。与协议 + 对齐基线 `types.AudioHeader` 对齐。

## 2. 范围与非目标

**范围**

- 独立 protocol 包
- 三套正交状态机
- Turn（`uplink_end_reason` / `turn_end_reason`）
- keepalive（周期 report）
- 正常路径本地校验 + 注入路径 `expected_server_drop`
- 文件切片上行 + UUID 对齐下行 + idle 结束
- 帧录制 + 音频落盘 + 结构化日志
- YAML 单设备配置 + 校验
- one-shot CLI（主命令 + `--inject`）

**非目标**

- 批量设备、Web UI、Scenario 引擎
- 完整连续模式流式发送
- 故障注入全集以外的类型
- 独立多进程 start / interrupt / status
- JSON downlink-ack 与非零 SleepMs
- `server_observed_drop`

## 3. 推荐目录结构（Phase 1）

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
│   └── check/
├── configs/
│   └── example_device.yaml
└── testdata/
    ├── golden_frames/
    └── audio/
```

语言以 `architecture.md` §7 为准：推荐 Go，收包冒烟通过后锁定。

## 4. protocol 包

- `AudioHeader` 固定 100 字节，小端，含 2 字节 padding。
- encode / decode；golden 对照对齐基线 `types.AudioHeader` 与协议手写样例。
- **禁止**把 `example/asr/mock.go` 的帧（`0x01` 首字节等）收进 golden。
- 管理首字节 ASCII `'1'`（0x31）；音频 `'0'`（0x30）；识别 `'4'` 与 `'{'`。

**mock.go 已知偏差（不要复制）：**

- 管理首字节 `0x01` 而非 `'1'`
- 不发 report
- 主路径不发 Stage=2
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

注入 `skip_register`：停在 Connected，不进入 Registering。  
注入 `skip_report`：停在 Registered，不进入 Reporting。

### 5.2 上行 Turn 状态机（UplinkTurn）

- **正常路径：** 仅 `Connection == Ready` 后可启动。
- **注入路径：** `Connection == Connected`（含其后任意状态）即可写帧，不要求 Ready。

```text
Idle
  → Speaking（Stage=1 分片中）
  → FinishingUpload（发 Stage=2）
  → WaitingReply

Speaking + 收到 Stage=4
  → FinishingUpload（立刻停发 Stage=1，补 Stage=2）

WaitingReply
  → Idle + tts_done     （至少一帧匹配 UUID 的 TTS，且下行 idle）
  → Idle + expected_server_drop  （timeout 内零匹配下行）

Speaking / WaitingReply
  → Interrupted（发 Stage=3 或 Stage=5）
  → Idle（强制换新 UUID，下一轮 Seq 从 0）
```

### 5.3 下行播放状态机（DownlinkPlayer）

```text
Idle
  → PlayingTTS（匹配 uplink_uuid 的 Stage=1）
  → Idle（idle 超时 / 新一轮 / 打断）
```

指令通道与三套状态机正交。读循环永不因落盘阻塞。

**硬规则**

- 正常路径新一轮必须 Seq=0 + Stage=1。
- 注入 `bad_seq` 故意从非 0 起，且必须出站。
- 收到 Stage=4 → FinishingUpload。
- 下行不依赖 Stage=2。
- 打断后换新 UUID，Seq 从 0。
- 下行头 `NeedAck=1` → 回 `'4'`，`SleepMs=0`。

## 6. Turn 数据结构

```yaml
turn:
  turn_id: "local-stable-id"
  device_id: "..."
  uplink_uuid: 123456
  injected_fault: null         # 注入路径才有
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

## 7. 配置 Schema

```yaml
device:
  enterprise: "demo"           # 须已在目标 core 配置
  device_type: "A3"            # 禁止 MH 前缀；须已有设备类型配置
  device_id: "sim_001"
  action: "chatbot"

  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"  # 新设备须能过 iot.Check，或用已有 status=1 设备
  location: ""

  playing_mode: 1

  audio:
    format: "wav"
    sample_rate: 16000
    slice_ms: 100
    max_payload_size: 51200    # 正常路径上限；注入 oversize 不受此限

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report"
    downlink_idle_timeout_sec: 20
    # 仅描述本示例机型是否预期下行带 NeedAck。
    # 客户端：只要头里 NeedAck=1 就必须回 '4'，不受本字段关闭。
    expect_downlink_need_ack: false

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

**最小联调前置**

- 对齐基线对应的 core 已启动（`architecture.md` §10）
- 企业配置、设备类型配置存在
- ICCID 能过 iot.Check，或设备已存在且 `status=1`
- 握手 Device 恰好三段，`Action=chatbot`

## 8. CLI（one-shot）

```bash
# 正常路径：握手→注册→report→切片→idle 收口→落盘→退出
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

# 注入路径（互斥）。坏帧必须出现在 frames.jsonl outbound。
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject skip_register
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject skip_report
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject bad_seq
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject oversize
go run ./cmd/speak --config ... --audio testdata/hello.wav --wait --inject bad_header

# 只做配置 + golden 自检，不连网
go run ./cmd/check --config configs/example_device.yaml
```

`--inject` 语义：

| 值 | 行为 |
|----|------|
| `skip_register` | 握手后不发 register，直接发音频 |
| `skip_report` | register 成功后不发 report，直接发音频 |
| `bad_seq` | Ready 后（或 Connected 后）本轮 Seq 从 >0 起 |
| `oversize` | 发出 payload > 50KiB 的音频帧 |
| `bad_header` | 发出长度/padding 错误的头 |

不提供独立多进程 `start` / `interrupt` / `status`。长连接推迟到 Phase 2。

## 9. 帧录制

```text
recordings/{device_id}/{turn_id}/
  ├── frames.jsonl      # 含 direction=outbound 的注入帧
  ├── uplink.<ext>
  ├── downlink.<ext>
  └── turn.json
```

## 10. 验收 Checklist

**正常路径**

- [ ] 握手成功（Device 三段 + Action=chatbot）
- [ ] register `code==0` 且 report `playingMode=1`
- [ ] AudioHeader 100 字节与 golden 一致（权威源为对齐基线，非 mock.go）
- [ ] Seq 从 0，Stage 1 → … → Stage 2
- [ ] 真实下行至少一帧 `'0'` TTS，接收栈未因 UTF-8 失败
- [ ] 按 UUID 对齐；idle 结束；出现 `tts_done`；`turn_end_reason=idle`
- [ ] 音频可落盘回放；`frames.jsonl` 可读
- [ ] 周期 report，空闲超过 360s 不掉线
- [ ] 下行 `NeedAck=1` 时回了 `'4'` 且 `SleepMs=0`
- [ ] 未注入时超限/坏格式 → `local_validation_error`，`frames.jsonl` **无**对应 outbound

**注入路径**（对齐基线：非 MH，bad_seq 为丢弃）

- [ ] `--inject skip_register|skip_report|bad_seq|oversize|bad_header` 时 outbound 有对应帧
- [ ] timeout 内出现 `expected_server_drop`，带同一 `injected_fault`
- [ ] 不出现服务端日志原因名事件
- [ ] 不把 mock.go 主路径当作验收标准
