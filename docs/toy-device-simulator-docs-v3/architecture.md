# 玩具设备模拟器 — 整体架构设计文档（v3）

> 吸收 v2 审查（`docs/toy-device-simulator-docs-v2/plan-review.md`）。规划层 P1 已关闭。

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS 对话能力。本模拟器用于：

- 模拟真实玩具设备完整协议行为
- 支持批量设备并发测试
- 设备参数可配置、可保存
- 提供 Agent 可编程操作能力
- 提供人工调试界面（配置 + 简单对话）

协议依据：`docs/toy-device-websocket-protocol.md`（基于下方对齐基线），主路径为 WebSocket `Action=chatbot`。

## 2. 设计原则

1. **协议忠实优先**：行为严格对齐协议文档与对齐基线中 `common/types/newProtocol.go` 的 `AudioHeader`。`example/asr/mock.go` 仅作为「现网脚本已知缺陷清单」，禁止作为 golden 或行为权威。
2. **失败可区分、可推断**：正常路径与注入路径分开（见 §4.2）。禁止假装观测服务端内部日志原因。
3. **Turn 是一等公民**：拆分 `uplink_end_reason` 与 `turn_end_reason`。
4. **API 优先，UI 后置**：UI 只消费同一套 Control API。
5. **文件音频为主路径**：实时麦克风与复杂转码后置。
6. **每阶段可独立测试**：有明确交付物与验收标准。
7. **状态机正交**：连接 / 上行 Turn / 下行播放 三套独立状态机。

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
│ - 事件总线（device_id + turn_id）                            │
│ - Scenario 执行与报告                                        │
│ - 故障注入入口（注入路径绕过本地守卫）                         │
│ - 单设备 Turn 并发控制（同时只允许一个 active uplink）        │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ DeviceInstance（每设备独立）                                  │
│ - 三套正交状态机：Connection / UplinkTurn / DownlinkPlayer   │
│ - 正常路径仅 Ready 后说话；注入路径 Connected 即可写帧         │
│ - keepalive（周期 report）                                   │
│ - 读循环不阻塞                                               │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ protocol 包                                                  │
│ AudioHeader 编解码、管理信封、ACK、无前缀 JSON 分流           │
│ golden 对照对齐基线 types.AudioHeader                         │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Persistence                                                  │
│ YAML + 帧录制 + 音频落盘（recordings/{device_id}/{turn_id}/） │
└─────────────────────────────────────────────────────────────┘
```

## 4. 核心抽象

### 4.1 Turn

```text
Turn
├── turn_id                # 本地稳定 ID
├── uplink_uuid            # 协议 UUID（1 .. 0x7FFFFFFF）
├── start_ts / end_ts
├── uplink_end_reason      # stage2 | interrupt | vad | error | timeout
├── turn_end_reason        # idle | interrupt | error | timeout
├── uplink_chunks[]
├── downlink_chunks[]
├── tts_path / format
├── asr_results[]          # 仅当设备类型开启 StreamingAsrTextReply
├── commands[]
├── errors[]
└── events[]
```

### 4.2 Event（双路径）

所有事件必须携带 `device_id` + `turn_id`（尚无 Turn 时用 `correlation_id`）。

**两条路径**

| 路径 | 何时 | 守卫 | 失败事件 |
|------|------|------|----------|
| 正常路径 | 未指定 `injected_fault` | 仅 `Connection == Ready` 后启动 UplinkTurn；非法配置/超限 **帧不得发出** | `local_validation_error` |
| 注入路径 | 显式 `injected_fault` | 握手成功（Connected）后即可写帧；绕过本地 max_payload / format / Seq 守卫 | 出站后 timeout → `expected_server_drop` |

注入帧必须出现在 `frames.jsonl` 的 outbound 记录中。测的是服务端静默丢包，不是本地拦截。

**错误与丢弃事件（主类型名写死）**

| 事件类型 | 含义 | 何时发出 |
|----------|------|----------|
| `local_validation_error` | 正常路径上发现非法，**帧未发出** | 配置非法、未注入时的超限/坏格式 |
| `expected_server_drop` | 帧**已发出**，timeout 内无匹配 UUID 的 ASR/TTS | 注入路径主结果；可附 `injected_fault` |
| `server_observed_drop` | 服务端日志或探针确认 | Phase 1 不做 |
| `protocol_error` | 能从下行帧解析出的错误 | 无前缀 JSON `Code=1` / `14007`、非法首字节 |
| `ack_failure` | register/report ACK `code != 0` | 含 `5001` |

`inferred_no_reply` 是 `expected_server_drop` 的别名，schema 可接受，实现枚举只保留一个主类型。

`injected_fault` 是元数据，不是独立成功标准。取值：`skip_register` | `skip_report` | `bad_seq` | `oversize` | `bad_header` | `bad_stage` | `dup_uuid`。

**禁止**客户端发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志原因名。

**下行完成（写死）**

- `tts_chunk`：收到匹配 `uplink_uuid` 的 `'0'` Stage=1 分片
- `tts_done`：本轮至少一帧 `tts_chunk`，且 DownlinkPlayer 因 idle（默认 20s）回到 Idle
- 发出上行结束之后 timeout 内零下行：`expected_server_drop`，`turn_end_reason=timeout`，**不是** `tts_done`，也不是 `idle`

其他正常事件：lifecycle、asr_result、command、vad、ack。

### 4.3 Scenario

主断言：`tts_done` + `turn_end_reason=idle`。`asr_contains` 仅当设备类型开启 `StreamingAsrTextReply` 时使用，标为 optional。

## 5. 协议硬约束清单

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text 帧，不得对下行做 UTF-8 文本解码 |
| AudioHeader | 100 字节（含 2 字节 padding），小端；golden 对照对齐基线 `types.AudioHeader`，禁止 mock.go |
| 首字节 | 管理 `'1'`（0x31）；音频 `'0'`（0x30）；ACK `'4'`；无前缀 JSON `'{'` |
| 新一轮 | Seq=0 + Stage=1；打断后换新 UUID 再从 0；**示例与负向用例禁止 MH 前缀机型** |
| bad_seq | 见 §10：本基线非 MH、无活跃 turn、Seq>0 → 服务端丢弃 → `expected_server_drop` |
| Stage=4 | 立刻停发 Stage=1 并补 Stage=2（Speaking → FinishingUpload） |
| 下行结束 | 无稳定 Stage=2；idle 超时（默认 20s）或打断/新一轮 |
| 心跳 | 周期 **report**（不要用 register 保活）；暴露 last_activity |
| 静默 | 正常路径 `local_validation_error`；注入路径 `expected_server_drop` |
| 无前缀 JSON | 与 `'1'` 严格分流；`Code=1/14007` → `protocol_error` |
| Register 限流 | 每 device_id 每秒一次，`5001` → `ack_failure` |
| 读循环 | 指令立即进事件总线，不因落盘阻塞 |
| 热更新 | 仅 `playingMode` 等少数可通过 report；身份字段必须重连 |
| ACK | 下行头 `NeedAck=1` 时必须回 `'4'` 且 `SleepMs=0`（与 YAML 机型预期无关）；JSON ACK 与非零 SleepMs 留 Phase 2 |
| 配置校验 | Device 三段、format、playing_mode ∈ {1,2,3}、UUID 范围 |

## 6. 音频策略

| 场景 | 推荐方式 |
|------|----------|
| 按键模式回归 | 预录文件切片 |
| 连续/唤醒 | 流式时间轴；silence 按真实节拍发静音/零采样帧 |
| UI 对话 | 上传文件为主 |
| 格式 | Core 保证 PCM/WAV + 直接喂已编码文件 |

## 7. 技术选型

**硬门槛**：能按字节接收 TextMessage 中的二进制音频。

- **Phase 1 Core 推荐 Go**（gorilla/websocket 或同等能力）。收包冒烟（真实 `'0'` TTS 一帧、未因 UTF-8 失败）通过后锁定为默认实现。
- 若用 Python：必须锁定库与版本、返回 raw bytes，并在真实 TTS 帧上通过非法 UTF-8 测试后才能作为选型。
- Python 更适合后续 Agent SDK / Scenario 执行层。

## 8. 分阶段总览

| 阶段 | 目标 | 主要交付 |
|------|------|----------|
| Phase 1 | 单设备闭环 + 注入通道 + 可推断失败 | protocol、正交状态机、Turn、one-shot CLI、帧录制 |
| Phase 2 | 批量 + API + Scenario + 连续模式 | Manager、REST/WS、speak_and_wait、精确 silence |
| Phase 3 | Web UI | 完全复用 Phase 2 API |
| Phase 4 | 按需 | 指标、回放、压测、探针 |

## 9. 推荐目录结构

```text
toy-device-simulator/
├── README.md
├── docs/
├── protocol/
├── core/
├── manager/
├── api/
├── scenario/
├── cli/                 # Phase 2 长连接控制；Phase 1 用 cmd/speak
├── ui/
├── configs/
├── testdata/
│   ├── golden_frames/   # 由对齐基线 AudioHeader 生成，禁止 mock.go
│   └── audio/
└── recordings/
    └── {device_id}/{turn_id}/
```

## 10. 对齐基线（已钉死）

本规划对齐下列实现。更换基线必须同步改本节、`bad_seq` 期望与 golden。

| 项 | 值 |
|----|----|
| 目标仓库 | `C:\Users\xie_f\projects\other\ai-creates-wealth` |
| commit | `5a02d70cdf964bdafea7be92495ad1d0a63499c5`（`5a02d70cd`，master：Merge #2357） |
| MH 前缀 Seq 例外 | **是**。`isNonResetSequenceNumberDeviceType` = `strings.HasPrefix(deviceType, "MH")` |
| 非 MH + 无活跃 turn + Stage=1 + Seq>0 | **丢弃**（`audioTurnIDForHeader` 返回空）。负向用例期望 `expected_server_drop` + `injected_fault=bad_seq` |
| 本地 core | `ws://127.0.0.1:8089/` |
| AudioHeader | 该 commit 的 `common/types/newProtocol.go` |

已知分叉（**不要**当成本规划权威）：

- `C:\Users\xie_f\projects\go\ai-creates-wealth` @ `19b62d97a7978f5d2face13ae59c3950c2e1c390` 无 MH 分支，`Stage=1` 时 Seq>0 也可能开新 turn。对该树跑 `bad_seq` 会得到相反结果。

协议文档中的 Seq=0 / MH 例外与本基线一致。
