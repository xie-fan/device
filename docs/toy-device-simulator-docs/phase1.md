# Phase 1 详细设计：单设备协议正确闭环

## 1. 目标

实现一个可独立运行的设备模拟器，完整跑通「握手 → 注册 → report → 按键模式发音频 → 接收 TTS」主路径，并保证失败可观测、与现网 mock 行为对齐。

## 2. 范围与非目标

**范围**

- 独立 protocol 包（AudioHeader + 管理消息 + 无前缀 JSON）
- DeviceInstance 完整状态机（主打 playMode=1）
- Turn 基础抽象
- keepalive
- 静默丢弃 → 明确事件
- 文件切片上行 + UUID 对齐下行 + idle 结束
- 全量帧录制 + 音频落盘 + 结构化日志
- YAML 单设备配置 + 校验
- CLI 驱动

**非目标**

- 批量设备
- Web UI
- Scenario / 断言引擎
- 完整连续模式流式发送
- 故障注入全集
- ACK 完整实现（可留骨架）

## 3. 推荐目录结构（Phase 1）

```text
toy-device-simulator/
├── protocol/
│   ├── header.py
│   ├── message.py
│   └── test_golden.py
├── core/
│   ├── device.py
│   ├── turn.py
│   ├── events.py
│   └── state.py
├── config/
│   ├── schema.py
│   └── loader.py
├── recording/
│   └── recorder.py
├── cli/
│   └── main.py
├── configs/
│   └── example_device.yaml
└── testdata/
    ├── golden_frames/
    └── audio/
```

## 4. protocol 包要求

- `AudioHeader` 固定 100 字节，小端，含 2 字节 padding。
- 必须提供 `encode` / `decode`，并有 golden test 与 `example/asr/mock.go` 发出的帧逐字节对比。
- 管理消息：`'1'` + `{"topic","data"}`。
- 无前缀 JSON（`{` 开头）与带 `'1'` 的管理消息严格分流。
- 支持识别 `'0'` 音频、`'4'` ACK。

## 5. DeviceInstance 状态机（关键转换）

```text
Disconnected
  → Connecting（握手，Header Device/Action）
  → Connected
  → Registering
  → Registered（收到 register/client 且 code==0）
  → Reporting
  → Ready
  → Speaking（Stage=1 上行中）
  → FinishingUpload（发 Stage=2）
  → WaitingReply
  → PlayingTTS（收到匹配 UUID 的 Stage=1 下行）
  → Idle
  → Interrupted（主动 Stage=3/5 或收到打断）

异常路径：
- 任意状态解析失败 / 静默丢弃 → 发 protocol_error 或 silent_drop 事件，不崩溃
- 空闲超过阈值 → 主动 keepalive 或进入 Disconnected
```

**硬规则**

- 新一轮必须从 Seq=0 + Stage=1 开始。
- 收到 Stage=4（即使 Phase1 主打 playMode=1，也要正确处理）→ 停发并补 Stage=2。
- 下行以 idle 超时（默认 20s）或新一轮/打断结束，不依赖 Stage=2。
- 读循环持续运行，不因落盘阻塞。

## 6. Turn 数据结构（草案）

```yaml
turn:
  turn_id: "uuid-or-local-id"
  device_id: "..."
  uplink_uuid: 123456
  start_ts: 1710000000.123
  end_ts: null
  finish_reason: null          # stage2 | interrupt | vad | error | timeout | idle
  uplink_chunks: []            # {seq, stage, payload_len, ts}
  downlink_chunks: []
  tts_path: null
  audio_format: "wav"
  asr_results: []              # {text, is_final, ts}
  commands: []
  errors: []
  events: []                   # 关联事件摘要
```

## 7. 配置 Schema（YAML 示例）

```yaml
device:
  enterprise: "demo"
  device_type: "MH-TOY"
  device_id: "sim_001"
  action: "chatbot"

  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  location: ""

  playing_mode: 1              # 1=按键

  audio:
    format: "wav"              # wav / pcm / mp3 / amr ...
    sample_rate: 16000
    slice_ms: 100
    max_payload_size: 51200

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    downlink_idle_timeout_sec: 20
    downlink_ack: false

  server:
    url: "ws://127.0.0.1:8089/"

  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings"
```

## 8. CLI 设计

```bash
# 启动并保持连接
python -m cli start --config configs/example_device.yaml

# 发送音频文件（自动切片 + 等结果）
python -m cli speak --config ... --audio testdata/hello.wav --wait

# 打断
python -m cli interrupt --config ...

# 查看当前状态 / 最近 Turn
python -m cli status --config ...

# 导出最近一次会话帧与音频
python -m cli dump --config ... --out ./out
```

## 9. 帧录制与落盘

每次会话建议落盘：

- `frames.jsonl`：每行一帧（方向、时间戳、类型、头字段摘要、payload_size、hash）
- `uplink_<uuid>.<ext>`
- `downlink_<uuid>.<ext>`
- `turn_<id>.json`：Turn 完整结果

## 10. 验收 Checklist

- [ ] 握手成功（Device 三段 + Action=chatbot）
- [ ] register 成功（code==0）并正确 report playingMode=1
- [ ] AudioHeader 100 字节，与 golden frame 逐字节一致
- [ ] Seq 从 0 开始，Stage 1 → … → Stage 2 完整上行
- [ ] 下行按 UUID 对齐，idle 超时正确结束本轮
- [ ] 上下行音频可落盘并回放
- [ ] 全量帧日志可读
- [ ] 故意不 register / 错误 Seq / 超大 payload 时有明确 silent_drop 或 protocol_error 事件
- [ ] 空闲超过心跳间隔不掉线
- [ ] 与 `example/asr/mock.go` 主路径行为一致
