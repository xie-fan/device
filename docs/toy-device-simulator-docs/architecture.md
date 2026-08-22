# 玩具设备模拟器 — 整体架构设计文档

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS 对话能力。本模拟器用于：

- 模拟真实玩具设备完整协议行为
- 支持批量设备并发测试
- 设备参数可配置、可保存
- 提供 Agent 可编程操作能力
- 提供人工调试界面（配置 + 简单对话）

协议依据：`玩具设备 WebSocket 交互协议`（基于 `ai-creates-wealth` 当前实现），主路径为 WebSocket `Action=chatbot`。

## 2. 设计原则

1. **协议忠实优先**：行为严格对齐协议文档与现网 `example/asr/mock.go`。
2. **失败必须可观测**：服务端静默丢弃的所有原因，模拟器必须发出明确事件 + 结构化日志。
3. **Turn 是一等公民**：一次说话轮次作为核心抽象，供 Agent / UI / Scenario 统一消费。
4. **API 优先，UI 后置**：所有能力先通过 Control API 暴露，UI 只消费同一套后端。
5. **文件音频为主路径**：实时麦克风与复杂转码后置。
6. **每阶段可独立测试**：有明确交付物与验收标准，完成后即可交付使用。

## 3. 总体架构

```text
┌─────────────────────────────────────────────────────────────┐
│ Control Plane                                                │
│  Web UI  │  Agent SDK / REST+WS API  │  CLI / Scenario Runner │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Device Manager                                               │
│ - 设备生命周期 / 批量创建 / 模板展开 / 错峰 / 资源限制         │
│ - 事件总线（强类型，带 device_id + turn_uuid）               │
│ - Scenario 执行与报告                                        │
│ - 故障注入入口                                               │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ DeviceInstance（每设备独立）                                  │
│ - 完整状态机 + keepalive                                     │
│ - Turn 管理（上行/下行/关联事件）                             │
│ - 读循环不阻塞（指令与 TTS 独立）                             │
│ - 静默丢弃 → 明确事件                                        │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ protocol 包（独立、可单测）                                   │
│ AudioHeader 编解码、管理信封、ACK、无前缀 JSON 分流           │
│ golden frame 与 mock.go 对齐                                 │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Persistence                                                  │
│ YAML 配置 + 帧录制 + 音频落盘（SQLite 会话历史可后置）        │
└─────────────────────────────────────────────────────────────┘
```

## 4. 核心抽象

### 4.1 Turn（一次对话轮次）

```text
Turn
├── uplink_uuid
├── start_ts / end_ts
├── finish_reason          # stage2 | interrupt | vad | error | timeout
├── uplink_chunks[]
├── downlink_chunks[]      # 按序
├── tts_path / format
├── asr_results[]          # 含 IsFinal
├── commands[]
├── errors[]
└── vad_notifications[]
```

### 4.2 Event（强类型事件）

所有事件必须携带 `device_id` + `turn_uuid`（或 `correlation_id`）。

主要类型：

- lifecycle：connected / registered / reported / disconnected
- protocol_error：header_parse_fail / unsupported_format / payload_too_large / no_active_turn / ...
- asr_result
- tts_chunk / tts_done
- command
- vad（Stage=4）
- ack
- silent_drop（各类静默丢弃原因）

### 4.3 Scenario

YAML/JSON 定义步骤序列 + 期望断言，支持批量执行与报告。

## 5. 协议硬约束清单

| 约束 | 要求 |
|------|------|
| AudioHeader | 固定 100 字节（含 2 字节 padding），小端；独立 protocol 包 + golden test |
| 新一轮 | 必须 Seq=0 + Stage=1；打断后换新 UUID 再从 0 开始；MH 例外单独分支 |
| Stage=4 | playMode=2/3 收到后立刻停发 Stage=1 并补 Stage=2 |
| 下行结束 | 没有稳定 Stage=2，用可配置 idle 超时（默认 20s）+ 打断/新一轮判定 |
| 心跳 | 主动 keepalive，防止 360s 空闲被踢；暴露 last_activity |
| 静默丢弃 | 全部变成明确事件（device_not_found / status_invalid / header_parse_fail / unsupported_format / payload_too_large / no_active_turn / rate_limited 等） |
| 无前缀 JSON | 与 `'1'` 管理信封严格分流 |
| Register 限流 | 同 device_id 每秒一次，批量必须错峰或重试 |
| 读循环 | 持续读所有下行，指令立即进事件总线，不因写文件/解码阻塞 |
| 热更新边界 | 仅 playingMode 等少数可通过 report 热更；身份字段必须重连 |
| ACK | 支持 `'4'` 二进制 + JSON；真实响应 SleepMs |
| 配置校验 | 启动时检查 Device 三段、format、playing_mode ∈ {1,2,3}、UUID 范围等 |

## 6. 音频策略

| 场景 | 推荐方式 |
|------|----------|
| 按键模式回归 | 预录文件切片（主路径） |
| 连续/唤醒模式 | 流式时间轴（语音段 + 可配置静音段） |
| UI 对话 | 上传文件为主；实时麦克风可选且后置 |
| 格式 | Core 保证 PCM/WAV + 直接喂已编码文件；转码放 helper |

## 7. 技术选型

- **Phase 1～3 推荐 Python（asyncio + websockets）**，利于 protocol 单测、音频、Scenario、Agent SDK。
- 若后续需要上千长连接压测，再评估 Go 实现 Core + HTTP/Python 客户端。
- 先统一技术栈，降低协作成本。

## 8. 分阶段总览

| 阶段 | 目标 | 主要交付 | 可独立测试 |
|------|------|----------|------------|
| Phase 1 | 单设备协议正确闭环 + 可观测 | protocol 包、DeviceInstance、Turn、CLI、帧录制、静默事件 | CLI 对真实服务跑通一轮对话 |
| Phase 2 | 批量 + API + Scenario + 连续模式基础 | Manager、REST/WS、speak_and_wait、Scenario、故障注入、流式发送 | 批量跑 Scenario 产出报告 |
| Phase 3 | Web UI 调试台 | 配置页、设备列表、对话页、日志流 | 纯 UI 完成配置+对话 |
| Phase 4 | 按需增强 | 指标、回放对比、更多格式、压测优化等 | 按需 |

## 9. 推荐整体目录结构

```text
toy-device-simulator/
├── README.md
├── docs/
│   ├── architecture.md
│   ├── phase1.md
│   ├── phase2.md
│   ├── phase3.md
│   └── phase4.md
├── protocol/
├── core/
├── manager/
├── api/
├── scenario/
├── cli/
├── ui/
├── configs/
├── testdata/
└── recordings/
```
