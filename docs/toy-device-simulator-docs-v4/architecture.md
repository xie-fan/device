# 玩具设备模拟器 — 整体架构设计文档（v4）

> 吸收 v3 审查。禁止再写「凡注入必 drop」或把 report 回显当 ACK。

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：模拟完整协议、批量并发、可配置可保存、Agent API、人工调试台。

协议依据：`docs/toy-device-websocket-protocol.md` 与 `architecture.md` §10 对齐基线。主路径：WebSocket `Action=chatbot`。

## 2. 设计原则

1. **协议忠实优先**：对齐协议文档与基线 `types.AudioHeader`。`example/asr/mock.go` 仅作缺陷清单。
2. **失败可区分、可推断**：正常路径 vs 注入路径。注入的期望以 **fault 矩阵** 为准，不得统一写成 drop。
3. **Turn 是一等公民**：拆分 `uplink_end_reason` 与 `turn_end_reason`。已发生的上行收口不得被后续打断改写。
4. **API 优先，UI 后置**。
5. **文件音频为主路径**；进入时间轴前必须是统一内部 PCM。
6. **每阶段可独立测试**。
7. **状态机正交**：Connection / UplinkTurn / DownlinkPlayer。

## 3. 总体架构

```text
Control Plane: Web UI | REST+WS API | CLI / Scenario
        │
Device Manager: 生命周期 / 错峰 / 事件总线 / 注入（须在对应步骤前配置）
        │
DeviceInstance: 三套状态机；正常路径仅 Ready 说话；注入路径按矩阵
        │
protocol: AudioHeader / 管理信封 / ACK / 无前缀 JSON
        │
Persistence: YAML + recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn

```text
Turn
├── turn_id / uplink_uuid
├── injected_fault            # 无则走正常路径
├── uplink_end_reason         # stage2 | interrupt | vad | error | timeout
│                             # 一旦因 Stage=2/3/5/vad 赋值，禁止事后改写
├── turn_end_reason           # idle | interrupt | error | timeout
├── uplink_chunks[] / downlink_chunks[]
├── tts_path / asr_results[] / commands[] / errors[] / events[]
```

**active turn**：从本轮上行第一帧发出起，直到 `turn_end_reason` 已赋值（Turn terminal）。WaitingReply / PlayingTTS 仍算 active。第二次 `speak` 默认 409。

### 4.2 Event

所有事件带 `device_id` + `turn_id`（无 Turn 时用 `correlation_id`）。

| 路径 | 何时 | 守卫 |
|------|------|------|
| 正常路径 | 无 `injected_fault` | 仅 Ready 后启动 UplinkTurn；非法配置 **帧不得发出** → `local_validation_error` |
| 注入路径 | 显式 `injected_fault` | 按 §4.4 矩阵：前置数据 + 精确出站字节 + **该行**期望事件。不是统一 drop |

| 事件类型 | 含义 |
|----------|------|
| `local_validation_error` | 正常路径非法，帧未发出 |
| `expected_server_drop` | 帧已发出，timeout 内无匹配 UUID 的 ASR/TTS。**仅当矩阵该行写 drop 时作为通过条件** |
| `server_observed_drop` | 探针确认；Phase 1 不做 |
| `protocol_error` | 下行无前缀 JSON `Code=1` / `14007`、非法首字节 |
| `ack_failure` | **仅** register ACK：`'1'` + topic `.../register/client` 且 `data.code != 0`（含 5001） |
| `report_echo` | `'1'` + topic 以 `/report/client` 结尾，`data` 为 `ReportData` 回显（不是 AckResponse） |

`inferred_no_reply` 是 `expected_server_drop` 的别名。

**禁止**发出 `device_not_found` 等服务端日志原因名。

**下行完成**

- `tts_chunk`：匹配 `uplink_uuid` 的 `'0'` Stage=1
- `tts_done`：至少一帧 `tts_chunk` 且 DownlinkPlayer 因 idle 回到 Idle
- 零下行 timeout：`expected_server_drop` 且 `turn_end_reason=timeout`（仅当本轮按矩阵允许用 drop 解释时）；不得标 `tts_done`

### 4.3 Scenario

主断言：`tts_done` + `turn_end_reason=idle`。`asr_contains` optional（需 `StreamingAsrTextReply`）。

### 4.4 注入 fault 矩阵（权威；Phase 1/2 必须遵守）

基线：`LookupCachedDevicePlayMode` 只查缓存或 Mongo `status=1`，**不要求本连接 register**。`ParseAudioHeader` 只拒绝 `len < 100`。report 下行是 `ReportData` 回显。

| fault | 前置数据 | 精确出站 | 期望 |
|-------|----------|----------|------|
| `skip_register` | **`device_id` 确认不在库**（新 ID）。禁止对已有 `status=1` 设备使用本 fault | 握手后不发 register；发合法 `'0'` 头 100 字节 + 音频 + Stage=2 | `expected_server_drop` + 该 fault。outbound 有音频帧 |
| `skip_report` | 允许已存在或刚 register 成功的设备 | 发 register 并等到 `code==0`；**不发 report**；发合法音频 | **不默认 drop**。允许 ASR/TTS。Connection 停在 Registered。不得因没 report 断言 drop |
| `bad_seq` | 非 MH 机型；无 active turn | `'0'` + 合法 100 字节头，`Stage=1`，`Seq>=1`，payload 合法 | `expected_server_drop`（基线无活跃 turn 则丢） |
| `oversize` | 注入路径 | `'0'` + 合法 100 字节头 + **payload > 51200** | `expected_server_drop` |
| `bad_header` | 注入路径 | `'0'` + **少于 100 字节**的后续（例如 10 字节任意数据）。禁止用「padding/magic 错误但总长仍 100」当本 fault | `expected_server_drop`（`ParseAudioHeader` 因长度失败） |
| `dup_uuid` | — | 可发送、可录帧 | **不作为 drop 验收**；只观察记录 |
| `bad_stage` | — | 可发送、可录帧 | **不作为 drop 验收** |

`skip_register` 若误用已有 `status=1` 的 ID：测试无效，应在配置校验阶段失败（`local_validation_error`：precondition 不满足），**不要发音频后强行等 drop**。

## 5. 协议硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text 帧 |
| AudioHeader | 100 字节；golden 对照基线；禁止 mock.go |
| 首字节 | `'1'` / `'0'` / `'4'` / `'{'` |
| 新一轮 | Seq=0 + Stage=1；示例禁止 MH 前缀 |
| bad_seq | 矩阵一行；非 MH 无活跃 turn → drop |
| Stage=4 | Speaking → FinishingUpload |
| 下行结束 | 无稳定 Stage=2；idle 默认 20s |
| 心跳 | 周期 **report** |
| Register | ACK `code==0` 才算 Registered；`5001` → `ack_failure` |
| Report | 匹配 `/report/client` **回显**进入 Ready；不是 ACK |
| 读循环 | 不因落盘阻塞 |
| ACK 音频 | 头 `NeedAck=1` 必须 `'4'` 且 `SleepMs=0` |
| 内部 PCM | **mono、signed 16-bit little-endian**；WAV 必须解封装后再切片/拼接/生成 silence |

## 6. 音频策略

| 场景 | 方式 |
|------|------|
| 按键回归 | 文件 → 解封装为内部 PCM → 按 slice_ms 切片 → 编码上线 |
| 连续模式时间轴 | 各段 WAV 解封装为同一内部 PCM，再插入 PCM silence，最后编码。禁止拼接带 RIFF 头的文件字节 |
| 线上格式无法生成合法静音帧 | 配置阶段拒绝该 format 的 silence |

## 7. 技术选型

硬门槛：按字节收 TextMessage 二进制音频。

Phase 1 Core **推荐 Go**（gorilla/websocket 或同等）。真实 `'0'` TTS 一帧冒烟通过后锁定。Python 须 raw bytes + 非法 UTF-8 测试后才能当选型。

## 8. 分阶段总览

| 阶段 | 交付 |
|------|------|
| Phase 1 | protocol、正交状态机、fault 矩阵、内部 PCM、one-shot CLI |
| Phase 2 | Manager、REST/WS；并发默认 **仅 reject**；`cancel_previous` 按 §4.1 原因字段；查询 API；silence 时间轴 |
| Phase 3 | UI 复用 Phase 2 |
| Phase 4 | 指标、回放、`queue` 策略、探针 |

## 9. 目录结构

与 v3 相同：`protocol/` `core/` `cmd/speak` `configs/` `testdata/golden_frames/` `recordings/{device_id}/{turn_id}/`。

## 10. 对齐基线

| 项 | 值 |
|----|----|
| 仓库 | `C:\Users\xie_f\projects\other\ai-creates-wealth` |
| commit | `5a02d70cdf964bdafea7be92495ad1d0a63499c5` |
| MH Seq 例外 | 是，`HasPrefix(deviceType, "MH")` |
| 非 MH + 无活跃 turn + Stage=1 + Seq>0 | 丢弃 |
| 音频准入 | `LookupCachedDevicePlayMode`：缓存或 DB `status=1`，与本连接是否 register 无关 |
| 头解析 | `ParseAudioHeader`：`len < 100` 失败；不校验 magic/padding |
| report 下行 | `ReportData` 回显，topic `.../report/client` |
| 本地 core | `ws://127.0.0.1:8089/` |

不要对齐 `projects/go/ai-creates-wealth`（Seq 行为分叉）。
