# 玩具设备模拟器规划审查

审查日期：2026-08-21  
审查范围：`architecture.md`、`phase1.md`、`phase2.md`、`phase3.md`、`phase4.md`  
对照依据：`docs/toy-device-websocket-protocol.md`，以及本地 `ai-creates-wealth` 的 `example/asr/mock.go`、`websocket/controller/handle.go`、`module/register/register.go`、`module/deviceAckModule/device_ack.go`  
审查对象：实施计划，不是代码 diff。本文不修改各阶段设计正文，只给出决策与必须改写的条款。

**决策：NEEDS_CHANGES**

阶段切分、Turn 作为一等公民、读循环不阻塞落盘、API 先于 UI，这些方向可以保留。按当前原文进入 Phase 1 会做出错误协议行为，或根本无法实现文档里的 CLI。先改完第 3 节五条阻塞项，再开工。

---

## 1. 结论摘要

| 类别 | 数量 | 处理 |
|------|------|------|
| 阻塞问题 | 5 | 必须改入规划后再实现 |
| 非阻塞说明 | 6 | 实现时一并写清，不挡修订后的 Phase 1 |
| 验证缺口 | 3 | 修订后用最小客户端补一次实连证据 |

可保留：

- 协议忠实优先、失败必须可观测、文件音频为主路径
- Phase 1 单设备闭环 → Phase 2 批量/API/Scenario → Phase 3 UI 复用 API
- 独立 `protocol` 包 + golden test（权威源必须换，见 B2）

不可按原文执行：

- Phase 1～3 无说明地推荐 Python `asyncio + websockets`
- 以 `example/asr/mock.go` 发出的帧做逐字节 golden
- Phase 1 多命令 CLI（`start` 长连接 + 独立 `speak`/`interrupt`/`status`）
- 示例 `device_type: MH-TOY`
- 客户端事件直接使用服务端静默丢弃原因名

---

## 2. 修订后即可开工的最小集合

把下面五条写进 `architecture.md` / `phase1.md` 后，Phase 1 可以开始。

1. **钉死 WebSocket 客户端能力**  
   必须能收非 UTF-8 的 Text 帧。写明库名与配置；若现有 Python 库做不到，Phase 1 改 Go，或使用返回原始字节的协议栈。不得默认 `websockets` 把 TEXT 当 Unicode 字符串。

2. **钉死协议权威源**  
   Golden 以 `common/types/newProtocol.go` 的 `AudioHeader` 与 `docs/toy-device-websocket-protocol.md` 为准。`example/asr/mock.go` 只作为「现网脚本缺陷清单」，禁止作为逐字节对照。

3. **收束 Phase 1 CLI**  
   只做单进程 one-shot：`speak` = 握手 → 注册 → report → 切片上行 → idle 收口 → 落盘。`start` / `interrupt` / `status` 要么放进同一进程（REPL 或本地 control port），要么明确推迟到 Phase 2。

4. **钉死对照实现与示例机型**  
   写明对齐的 `ai-creates-wealth` 路径与 commit。示例 `device_type` 不得以 `MH` 开头。补充联调前置：企业配置、设备类型配置、ICCID/IoT、Mongo `status=1`。

5. **改写静默丢弃事件模型**  
   客户端只发 `inferred_no_reply`（加本地注入上下文）。`Code=14007` 与注册 `code=5001` 从 `silent_drop` 挪到协议可见错误 / ACK。

---

## 3. 阻塞问题

### B1. Python 默认 WebSocket 栈收不下真实下行

**位置：** `architecture.md` §7；`phase1.md` §1、§10

**事实：**

- 协议 §2.1：服务端下行一律 `TextMessage`，payload 可以是二进制音频。
- `example/asr/mock.go`：`conn.WriteMessage(websocket.TextMessage, fullMsg)`，帧内是二进制头 + 音频。
- gorilla/websocket 允许对 TEXT 写入任意 `[]byte`。Python `websockets` 等 RFC 实现对 OP_TEXT 做 UTF-8 解码。

**后果：** Phase 1 验收「CLI 对真实服务跑通一轮对话」会在收 TTS 时失败，与是否编对 AudioHeader 无关。

**应写入规划：**

- 客户端必须按字节读帧，不得把下行 TEXT 当 UTF-8 文本。
- 写明候选：Go（gorilla 同类）、或可关闭 UTF-8 校验 / 返回 `bytes` 的 Python 栈，并要求用真实下行 TTS 帧做冒烟。
- 在选定栈未验证前，不得把「Python 利于单测」写成已定技术选型。

### B2. 验收标准自相矛盾：对齐 mock.go 还是对齐协议

**位置：** `phase1.md` §4、§10；`architecture.md` §2

**事实（`example/asr/mock.go`）：**

| 行为 | mock.go | 协议 / Phase 1 清单 |
|------|---------|---------------------|
| 管理消息首字节 | `[1]byte{1}` → `0x01` | `'1'`（`0x31`） |
| report | 不发送 | 必须 report `playingMode` |
| Stage=2 | `sendFinish` 被注释，主路径不发 | 按键模式必须发 Stage=2 |
| UUID | 写死 `1` | `1 .. 0x7FFFFFFF`，每轮新值 |
| 切片间隔注释 | 注释写 100ms，代码 `Sleep(300ms)` | 约 100ms 一片为实践值 |

服务端 `ManageHandle` 会剥掉首字节再 JSON 反序列化，因此 `0x01` 注册「碰巧能用」，不是合法协议。

**应写入规划：**

- Golden test 对照：`AudioHeader` 100 字节小端布局（含 2 字节 padding）、`'0'`/`'1'`/`'4'`/`'{'` 分流、与手写 hex 样例或从 `binary.Write(types.AudioHeader)` 生成的帧。
- 另开一节「现网 mock.go 已知偏差」，实现时不要复制。
- 删除「与 mock.go 发出的帧逐字节对比」「与 mock.go 主路径行为一致」作为验收项。

### B3. Phase 1 CLI 进程模型无法按文档实现

**位置：** `phase1.md` §8

文档同时要求：

- `python -m cli start --config ...` 启动并保持连接
- `python -m cli speak|interrupt|status --config ...` 另起进程、只传同一份 YAML

没有 control socket、HTTP 或共享运行时，后三个命令无法找到 `start` 里的连接。REST/WS 控制面属于 Phase 2。

**应写入规划（二选一，写死一种）：**

- **推荐（Phase 1 最小）：** 只提供 one-shot  
  `speak --config --audio [--wait]`  
  一次进程走完握手到落盘；不要承诺独立的 `start`/`interrupt`/`status`。
- **若必须长连接：** Phase 1 增加本地 control port（例如 `127.0.0.1:随机或配置端口`），`speak`/`interrupt`/`status` 打这个端口；并写清单实例锁，避免同一 `device_id` 双连。

### B4. 示例机型 `MH-TOY` 会绕过 Seq=0，且服务端版本未钉死

**位置：** `phase1.md` §7；`architecture.md` §5；协议 §7.1

**事实：**

- 协议：新一轮必须 `Seq=0` + `Stage=1`；设备类型前缀 `MH` 会把非 0 序号当新一轮。
- `projects/other/ai-creates-wealth`：`isNonResetSequenceNumberDeviceType` 为 `strings.HasPrefix(deviceType, "MH")`。`MH-TOY` 会命中。
- `projects/go/ai-creates-wealth`：`audioTurnIDForHeader` 无该 MH 分支；`Stage=1` 时 Seq>0 也可能开新 turn。两棵树行为不一致。

用 `MH-TOY` 做默认示例，Phase 1「错误 Seq 有明确事件」会假通过或与「当前实现」对不上。

**应写入规划：**

- 示例改为不含 `MH` 前缀的类型，例如与现网脚本同类的 `A3`，或文档专用的 `SIM-TOY`（须在 core 里先有设备类型配置）。
- 在 `architecture.md` 增加「对齐基线」：仓库路径、commit、是否含 MH Seq 例外。
- Seq 负向用例必须在非 MH 类型上跑。

### B5. 客户端无法按文档发出服务端静默丢弃原因

**位置：** `architecture.md` §2、§4.2、§5；`phase1.md` §10；协议 §7.5、§9.4

服务端对下列情况只打日志、不下发错误帧：设备不在库或 `status != 1`、头解析失败、格式不支持、payload > 50 KiB、Seq>0 且无活跃 turn（非 MH）。

客户端看不见这些原因。能看见的是：

| 服务端行为 | 设备侧实际可见 |
|------------|----------------|
| 真静默丢包 | 超时无 ASR/TTS |
| 用量拒绝 | 无前缀 JSON `Code=14007`，可能还有提示音 |
| 注册限流 | `'1'` ACK `code=5001` |
| 音频处理失败 | 无前缀 JSON `Code=1` |

把 `rate_limited` 放进 `silent_drop` 是分类错误。Phase 1 验收「故意不 register / 错误 Seq / 超大 payload 时有明确 silent_drop」若写成服务端原因枚举，实现会造假事件。

**应写入规划：**

```text
protocol_error     能从帧解析出的错误（无前缀 JSON、非法首字节、头长度不足）
ack_failure        register/report ACK code != 0（含 5001）
inferred_no_reply  本地已发送、在 timeout 内无匹配 UUID 的 ASR/TTS
injected_fault     故障注入元数据（skip_register / bad_seq / oversize / ...）
```

`inferred_no_reply` 可附带 `suspected_cause`（来自注入意图，不是服务端回传）。Scenario 主断言用「无回复 + 注入类型」，不要断言服务端内部日志字符串。

---

## 4. 非阻塞说明

### N1. `finish_reason` 混用上行结束与下行结束

`stage2` / `vad` 描述上行收口，`idle` 描述下行结束。Phase 2 示例断言 `turn_finish_reason: idle`，与「已发 Stage=2」不是同一时刻。

建议拆成 `uplink_end_reason` 与 `turn_end_reason`。

### N2. 状态机缺打断回流

`Interrupted` 未画回 `Ready`/`Idle`，也未写「必须换新 UUID，Seq 从 0」。实现时容易在同一 UUID 上续发分片。

### N3. keepalive 未指定报文

任意入站消息刷新 360s 心跳。周期再 `register` 会撞每秒一次限流；乱发音频可能被静默丢但仍续命。应指定周期 `report`，或一条明确合法、无副作用的保活帧。

### N4. ACK 硬约束与 Phase 1 非目标冲突

示例 `downlink_ack: false` 时 Phase 1 可暂缓完整 ACK。设备类型 `DownlinkAck=true` 时头里 `NeedAck=1`，`SleepMs` 会改下发间隔。Phase 1 建议：若收到 `NeedAck=1`，回 `'4'` 且 `SleepMs=0`；JSON ACK 可留 Phase 2。

### N5. Phase 2 默认 Scenario 把 ASR 文本当主断言

流式 ASR 仅当设备类型 `StreamingAsrTextReply=true`。多数玩具类型会假失败。主断言应为：匹配 UUID 的 TTS 分片 + `turn_end_reason=idle`；`asr_contains` 标成可选。

### N6. 真实服务联调前置几乎没写

需要：企业配置存在、设备类型配置存在、新设备 `nic_iccid` 能过 `iot.Check`（或使用已存在且 `status=1` 的设备）、握手 `Device` 恰好三段、`Action=chatbot`。应在 Phase 1 增加「最小联调环境」一节（本地 core 地址、种子数据、禁止用未配置的 `demo/MH-TOY`）。

---

## 5. 验证缺口

| ID | 缺口 | 影响 |
|----|------|------|
| G1 | 本次审查未对真实 core 做 Python/Go 拨号 | B1 来自协议与库契约，不是本机收包日志。修订技术栈后须用最小客户端打一次 TTS 帧。 |
| G2 | 「当前实现」未钉仓库与 commit | 本地至少两棵 `ai-creates-wealth` 在 Seq=0 / MH 前缀上已分叉。Golden 与静默丢包用例会随树漂移。 |
| G3 | 本仓库没有 `testdata/golden_frames` 或示例音频 | 无法在审查时核对样例字节；Phase 1 目录结构目前只存在于文档。 |

---

## 6. 建议改入各文件的条文草案

以下可直接粘贴进对应文档，替代冲突段落。

### 6.1 `architecture.md` §5 硬约束（事件与 ACK）

- 静默丢包：客户端发 `inferred_no_reply` + 可选 `injected_fault`，不声称掌握服务端内部原因。
- 无前缀 JSON `Code=1` / `14007` 记为 `protocol_error`，不是 `silent_drop`。
- 注册 `code=5001` 记为 `ack_failure`，批量错峰或重试。
- ACK：设备类型开启 `DownlinkAck` 时必须回 `'4'`；`SleepMs` 默认 0，除非 Scenario 显式模拟节流。
- 对齐基线：写明 `ai-creates-wealth` 路径与 commit；Seq=0 规则以该 commit 的 `audioTurnIDForHeader` 为准。

### 6.2 `architecture.md` §7 技术选型

Phase 1 技术选型以「能收非 UTF-8 Text 帧」为门槛。Python 仅在选定库通过真实 TTS 下行冒烟后采用；否则 Phase 1 Core 用 Go。不要在门槛验证前写死 `asyncio + websockets`。

### 6.3 `phase1.md` §2 非目标

- 删除「ACK 完整实现（可留骨架）」这种含糊表述。
- 改为：JSON `downlink-ack` 与非零 `SleepMs` 模拟留 Phase 2；二进制 `'4'` 在 `NeedAck=1` 时 Phase 1 必须回，`SleepMs=0`。
- CLI：独立多进程 `start`+`speak` 不是 Phase 1 目标。

### 6.4 `phase1.md` §4 protocol 包

- Golden 对照 `types.AudioHeader` 与协议文档手写样例。
- 明确管理首字节为 ASCII `'1'`（`0x31`），音频为 ASCII `'0'`（`0x30`）。
- 禁止把 mock.go 的 `0x01` 注册帧收进 golden。

### 6.5 `phase1.md` §7 示例配置

- `device_type` 改为非 `MH` 前缀（须已在目标 core 配置）。
- 增加 `uuid.min` / `uuid.max`（`1` .. `0x7FFFFFFF`）。
- `keepalive` 写明报文类型（建议周期 `report`）。
- `server` 增加「联调前置」注释：企业、设备类型、ICCID 或已有 `status=1` 设备。

### 6.6 `phase1.md` §8 CLI

推荐最小接口：

```text
# 单进程：连接 → 注册 → report → 发音频 → 等 idle → 落盘 → 退出
python -m cli speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

# 可选：只校验配置与 golden，不连网
python -m cli check --config configs/example_device.yaml
```

长连接与打断放到 Phase 2，或在本阶段增加已文档化的 control port。

### 6.7 `phase1.md` §10 验收

保留：握手三段、`register code==0`、report、100 字节头、Seq 从 0、Stage 1→2、UUID 对齐下行、idle 结束、落盘、心跳不掉线。

删除：与 mock.go 逐字节/主路径一致。

改写负向用例：跳过 register / 错误 Seq / 超大 payload 时，在 timeout 内出现 `inferred_no_reply`，事件带对应 `injected_fault`；不要求事件名等于服务端日志原因。

增加：用真实下行至少收到一帧 `'0'` TTS，且接收栈未因 UTF-8 失败。

### 6.8 `phase2.md` §5 Scenario 示例

主断言改为 `event_received: tts_chunk`（或 `tts_done`）+ `turn_end_reason: idle`。`asr_contains` 标为 `optional: true` 或加设备类型前提 `streaming_asr_text_reply: true`。

---

## 7. 明确不在本次审查范围

- 未实现模拟器代码（本仓库当前只有文档）。
- MQTT 路径、拍照 `'2'`、转发 `'3'`（协议已排除；Phase 4 可选）。
- 对 Phase 3 UI 视觉稿、Phase 4 指标方案的产品评价。
- 未跑真实 core、未生成 golden 文件（见第 5 节）。

---

## 8. 建议下一步

1. 按第 2、第 6 节改 `architecture.md` 与 `phase1.md`（必要时改 `phase2.md` 示例 Scenario）。
2. 选定 WS 栈后，用最小客户端对 `ws://127.0.0.1:8089` 收一帧 TTS，关闭 G1。
3. 在规划中写死 `ai-creates-wealth` commit，关闭 G2。
4. 再开实现任务；实现期间不要把 mock.go 当规范客户端。
