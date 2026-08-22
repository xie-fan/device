# 玩具设备模拟器 — 整体架构设计文档（审查后修订版）

> 修订依据：两份 plan-review（2026-08-21）。已关闭所有 P1 阻塞项。

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS 对话能力。本模拟器用于：

- 模拟真实玩具设备完整协议行为
- 支持批量设备并发测试
- 设备参数可配置、可保存
- 提供 Agent 可编程操作能力
- 提供人工调试界面（配置 + 简单对话）

协议依据：`玩具设备 WebSocket 交互协议`（基于 `ai-creates-wealth` 当前实现），主路径为 WebSocket `Action=chatbot`。

## 2. 设计原则

1. **协议忠实优先**：行为严格对齐协议文档与 `common/types/newProtocol.go` 的 `AudioHeader` 定义。`example/asr/mock.go` 仅作为「现网脚本已知缺陷清单」，禁止作为 golden 或行为权威。
2. **失败可区分、可推断**：
   - `local_validation_error`：本地发现的非法帧/配置/注入错误
   - `expected_server_drop`：根据协议推断服务端应丢弃，附 `injected_fault` 上下文；不声称已观测到服务端日志
   - `server_observed_drop`：仅当接入服务端日志或测试探针时可用（Phase 1 不做）
3. **Turn 是一等公民**：一次说话轮次作为核心抽象。拆分 `uplink_end_reason` 与 `turn_end_reason`。
4. **API 优先，UI 后置**：所有能力先通过 Control API 暴露，UI 只消费同一套后端。
5. **文件音频为主路径**：实时麦克风与复杂转码后置。
6. **每阶段可独立测试**：有明确交付物与验收标准。
7. **状态机正交**：连接 / 上行 Turn / 下行播放 三套独立状态机，禁止单张线性图覆盖所有并发。

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
│ - 事件总线（强类型，带 device_id + turn_id）                 │
│ - Scenario 执行与报告                                        │
│ - 故障注入入口                                               │
│ - 单设备 Turn 并发控制（同时只允许一个 active uplink）        │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ DeviceInstance（每设备独立）                                  │
│ - 三套正交状态机：Connection / UplinkTurn / DownlinkPlayer   │
│ - keepalive                                                  │
│ - Turn 管理（上行/下行/关联事件）                             │
│ - 读循环不阻塞（指令与 TTS 独立通道）                         │
│ - 本地校验 + expected_server_drop 推断                       │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ protocol 包（独立、可单测）                                   │
│ AudioHeader 编解码、管理信封、ACK、无前缀 JSON 分流           │
│ golden 对照 types.AudioHeader + 协议文档手写/生成样例         │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Persistence                                                  │
│ YAML 配置 + 帧录制 + 音频落盘（路径含 device_id）             │
│ SQLite 会话历史可后置                                        │
└─────────────────────────────────────────────────────────────┘
```

## 4. 核心抽象

### 4.1 Turn（一次对话轮次）

```text
Turn
├── turn_id                # 本地生成的稳定 ID
├── uplink_uuid            # 协议中的 UUID（1 .. 0x7FFFFFFF）
├── start_ts / end_ts
├── uplink_end_reason      # stage2 | interrupt | vad | error | timeout
├── turn_end_reason        # idle | interrupt | error | timeout
├── uplink_chunks[]
├── downlink_chunks[]      # 按序
├── tts_path / format
├── asr_results[]          # 含 IsFinal（仅当设备类型开启时）
├── commands[]
├── errors[]
└── events[]
```

### 4.2 Event（强类型，修订后）

所有事件必须携带 `device_id` + `turn_id`（或 correlation_id）。

**错误与丢弃相关：**

| 事件类型 | 含义 | 何时发出 |
|----------|------|----------|
| `local_validation_error` | 本地发现非法，帧未发出或本地拒绝 | 头长度不足、格式不支持、payload 超限、自己注入的坏帧等 |
| `expected_server_drop` / `inferred_no_reply` | 已发送，timeout 内无匹配 UUID 的 ASR/TTS | 可附带 `injected_fault`（skip_register / bad_seq / oversize 等） |
| `server_observed_drop` | 通过服务端日志或测试探针确认 | Phase 1 不做；仅当接入探针时允许 |
| `protocol_error` | 能从帧解析出的错误 | 无前缀 JSON Code=1 / 14007、非法首字节等 |
| `ack_failure` | register/report ACK code != 0 | 含 5001 限流 |

其他正常事件：lifecycle、asr_result、tts_chunk、tts_done、command、vad、ack 等。

**禁止**：客户端直接发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端内部日志原因名。

### 4.3 Scenario

YAML/JSON 定义步骤序列 + 期望断言。主断言应优先使用「匹配 UUID 的 TTS + turn_end_reason=idle」，`asr_contains` 标为可选（仅当设备类型开启 StreamingAsrTextReply）。

## 5. 协议硬约束清单（修订后）

| 约束 | 要求 |
|------|------|
| 收包 | 客户端必须按字节处理 Text 帧，不得对下行做 UTF-8 文本解码 |
| AudioHeader | 固定 100 字节（含 2 字节 padding），小端；独立 protocol 包 + golden test（对照 types.AudioHeader，禁止 mock.go） |
| 首字节 | 管理消息 ASCII `'1'`（0x31）；音频 ASCII `'0'`（0x30）；ACK `'4'`；无前缀 JSON `'{'` |
| 新一轮 | 必须 Seq=0 + Stage=1；打断后换新 UUID 再从 0 开始；**示例与负向用例禁止使用 MH 前缀机型** |
| Stage=4 | playMode=2/3 收到后立刻停发 Stage=1 并补 Stage=2 |
| 下行结束 | 没有稳定 Stage=2，用可配置 idle 超时（默认 20s）+ 打断/新一轮判定 |
| 心跳 | 主动 keepalive（建议周期 report，避免 register 限流）；暴露 last_activity |
| 静默相关 | 只允许 local_validation_error / expected_server_drop（inferred_no_reply）；禁止假装观测服务端内部原因 |
| 无前缀 JSON | 与 `'1'` 管理信封严格分流；Code=1/14007 记为 protocol_error |
| Register 限流 | 同 device_id 每秒一次，code=5001 记为 ack_failure；批量必须错峰或重试 |
| 读循环 | 持续读所有下行，指令立即进事件总线，不因写文件/解码阻塞 |
| 热更新边界 | 仅 playingMode 等少数可通过 report 热更；身份字段必须重连 |
| ACK | 设备类型开启 DownlinkAck 且 NeedAck=1 时，Phase 1 必须回 `'4'` 且 SleepMs=0；JSON ACK 与非零 SleepMs 模拟留 Phase 2 |
| 配置校验 | 启动时检查 Device 三段、format、playing_mode ∈ {1,2,3}、UUID 范围等 |
| 对齐基线 | 必须在文档中写明目标 `ai-creates-wealth` 的仓库路径与 commit；Seq=0 / MH 例外以该 commit 为准 |

## 6. 音频策略

| 场景 | 推荐方式 |
|------|----------|
| 按键模式回归 | 预录文件切片（主路径） |
| 连续/唤醒模式 | 流式时间轴；silence 必须按真实节奏生成并发送静音/零采样帧（详见 Phase 2） |
| UI 对话 | 上传文件为主；实时麦克风可选且后置 |
| 格式 | Core 保证 PCM/WAV + 直接喂已编码文件；转码放 helper |

## 7. 技术选型（修订后）

**硬门槛**：客户端必须能按字节接收服务端 TextMessage 中的二进制音频，不得对 TEXT 帧做默认 UTF-8 解码。

- **Phase 1 Core 推荐 Go**（gorilla/websocket 或同等能力，已证明可正确处理 TEXT + 二进制 payload，也与现网一致）。
- 若使用 Python，必须显式锁定库与版本，使用返回 raw bytes 的接收方式，并在真实 TTS 下行帧上通过非法 UTF-8 集成测试后才能作为选型。
- 未通过收包冒烟验证前，不得写死任何语言为默认实现。
- Python 更适合后续 Agent SDK / Scenario 执行层。

## 8. 分阶段总览

| 阶段 | 目标 | 主要交付 | 可独立测试 |
|------|------|----------|------------|
| Phase 1 | 单设备协议正确闭环 + 可推断失败 | protocol 包、正交状态机、Turn、one-shot CLI、帧录制 | one-shot 对真实服务跑通一轮对话 |
| Phase 2 | 批量 + API + Scenario + 连续模式 | Manager、REST/WS（含并发规则与查询接口）、speak_and_wait、Scenario、故障注入、精确 silence | 批量跑 Scenario 产出报告 |
| Phase 3 | Web UI 调试台 | 配置页、设备列表、对话页、日志流（完全复用 Phase 2 已补齐的 API） | 纯 UI 完成配置+对话 |
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
│   ├── golden_frames/      # 由 types.AudioHeader 生成，禁止来自 mock.go
│   └── audio/
└── recordings/
    └── {device_id}/{turn_id}/
```

## 10. 对齐基线（必须填写）

实现前请在此处钉死：

- 目标仓库路径：`____________________________`
- commit / tag：`____________________________`
- 是否包含 MH 前缀 Seq 例外：是 / 否（以该 commit 的 `audioTurnIDForHeader` 为准）
- 本地 core 默认地址：`ws://127.0.0.1:8089/`

未填写前不得开始 Phase 1 实现。
