# 玩具设备模拟器规划审查（v2）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v2/` 下 `architecture.md`、`phase1.md`、`phase2.md`、`phase3.md`、`phase4.md`  
对照依据：

- 上一轮审查：`docs/toy-device-simulator-docs/plan-review.md`（v1，5 条 P1）
- `docs/toy-device-websocket-protocol.md`
- 本地 `ai-creates-wealth`：`example/asr/mock.go`、`websocket/controller/handle.go`（至少两棵树：`projects/other/ai-creates-wealth` 与 `projects/go/ai-creates-wealth`）

审查对象：v2 实施计划，不是代码 diff。本文不修改各阶段设计正文，只给出决策与必须改写的条款。

**决策：NEEDS_CHANGES**

v2 已关闭上一轮大部分 P1：收包栈、golden 权威源、one-shot CLI、事件分类、MH 示例机型。方向可以保留。仍不能按 README「关闭所有 P1」开工：故障注入与正常状态机互相排斥，对齐 commit 仍是空白。先改完第 3 节两条阻塞项。

---

## 1. 结论摘要

| 类别 | 数量 | 处理 |
|------|------|------|
| 上一轮 P1 已关闭 | 4（原 B1/B2/B3/B5）；原 B4 只关了一半 | 见第 7 节 |
| 本轮阻塞 | 2 | 必须改入 v2 后再实现 |
| 非阻塞说明 | 6 | 实现时一并写清 |
| 验证缺口 | 3 | 填 commit + 实连收一帧 TTS |

可保留：

- 按字节收 Text 帧；Phase 1 推荐 Go
- Golden 对照 `types.AudioHeader`；mock.go 仅作缺陷清单
- one-shot `speak`；长连接推迟到 Phase 2
- 事件：`local_validation_error` / `expected_server_drop` / `protocol_error` / `ack_failure`
- 示例 `device_type: A3`（禁止 MH 前缀）
- 正交状态机；`uplink_end_reason` / `turn_end_reason` 拆分
- silence 必须按节拍发静音帧；Phase 3 查询 API 前移到 Phase 2

不可按原文执行：

- 在 `Connection == Ready` 约束下做 `skip_register` / `skip_report` 负向验收
- 把 oversize / 非法格式既当「帧未发出」又当「已发送后的 expected_server_drop」
- 对齐基线空白时实现 `bad_seq`，或宣称 P1 已全部关闭

---

## 2. 再改两处即可开工

1. **注入通道**  
   允许从 Connected 发音频。注入帧绕过本地 `max_payload` / format 拒绝，必须真实发出，再用 `expected_server_drop` 等回复。正常路径仍只在 Ready 后说话。

2. **对齐基线填实**  
   在 `architecture.md` §10 写上目标仓库路径与 commit，并据此写死 `bad_seq` 期望（丢弃或开新 turn）。在此之前不要写「P1 已全部关闭」。

---

## 3. 阻塞问题

### B1. 负向注入与正常状态机互相排斥

**位置：** `phase1.md` §5.2、§7、§10；`architecture.md` §4.2

**冲突：**

| 文档条款 | 含义 |
|----------|------|
| UplinkTurn「仅在 Connection == Ready 后可启动」 | Ready 要先 register `code==0` 再 report |
| 验收：注入 `skip_register` / `skip_report` 后出现 `expected_server_drop` | 必须发出音频，等服务端静默丢 |
| `local_validation_error`：头长度不足、格式不支持、payload 超限，「帧未发出或本地拒绝」 | 坏帧不出站 |
| 验收：注入 `oversize` 后出现 `expected_server_drop` | 坏帧必须出站 |

`skip_register` 到不了 Ready，按状态机发不出音频，服务端不会静默丢，客户端也等不到 `expected_server_drop`。  
`max_payload_size: 51200` 若在本地拦 oversize，同样测不到服务端丢包。

**应写入规划：**

- 分两条路径：
  - **正常路径：** 仅 Ready 后启动 UplinkTurn；配置非法 → `local_validation_error`，帧不出站。
  - **注入路径：** 可在 Connected 之后任意时刻强制写帧；绕过本地 payload/format/Seq 守卫；发出后按 timeout 等 `expected_server_drop` + `injected_fault`。
- `skip_register`：握手后不发 register，直接发音频。
- `skip_report`：register 成功后不发 report，直接发音频。
- `oversize` / `bad_seq` / 坏头：必须出现在 `frames.jsonl` 的 outbound 记录里。

### B2. 对齐基线仍空，却宣称 P1 已关闭

**位置：** `architecture.md` §10；`README.md`；`phase1.md` §7（`A3`）与 §10（`bad_seq`）

**事实：**

- §10 的仓库路径、commit、是否含 MH Seq 例外仍是填空。文档写「未填写前不得开始 Phase 1」。
- README 写「关闭所有 P1 阻塞项」。上一轮原 B4 要求同时改示例机型 **并钉死 commit**。机型已改为 `A3`；commit 未填。
- `projects/other/ai-creates-wealth`：`isNonResetSequenceNumberDeviceType` 为 `HasPrefix("MH")`；非 MH 且 Seq>0、无活跃 turn 时 `audioTurnIDForHeader` 返回 `("", false)`，音频被丢。
- `projects/go/ai-creates-wealth`：无 MH 分支；`Stage==Uploading` 时即使 Seq>0 也可能开新 turn。

因此 `bad_seq` + 示例 `A3` 的期望随树相反：一边应 `expected_server_drop`，另一边可能跑通一轮对话。

**应写入规划：**

- 填实路径与 commit（或 tag）。
- 用该 commit 的 `audioTurnIDForHeader` 写死一句话：非 MH 机型、无活跃 turn、Seq>0 是丢弃还是新一轮。
- README 改为「v2 已关上一轮多数 P1；剩余项见 `plan-review.md`」，不要写全部关闭。

---

## 4. 非阻塞说明

### N1. 事件双名

`expected_server_drop` 与 `inferred_no_reply` 并用。选定一个为主类型（建议 `expected_server_drop`），另一个在 schema 里标成别名。

### N2. `tts_done` 未定义

协议没有稳定的下行 Stage=2。Phase 2 Scenario 主断言却是 `event: tts_done`。

建议写死：

- `tts_chunk`：收到匹配 `uplink_uuid` 的 `'0'` Stage=1 分片
- `tts_done`：本轮至少一帧 `tts_chunk`，且 DownlinkPlayer 因 idle（默认 20s）回到 Idle
- 发出 Stage=2 后 timeout 内零下行 → `expected_server_drop`，`turn_end_reason=timeout`，不是 `tts_done`，也不是 `idle`

### N3. `downlink_ack: false` 语义含糊

硬约束：设备类型开启 DownlinkAck 且头 `NeedAck=1` 时，Phase 1 必须回 `'4'` 且 `SleepMs=0`。  
示例 YAML 写 `downlink_ack: false`。若实现成客户端开关，真机类型带 NeedAck 时会违规。

该字段只应描述「本示例机型是否预期下行带 NeedAck」。客户端行为：见到 `NeedAck=1` 就回 ACK。

### N4. Stage=4 不在状态图里

硬规则要求收到 Stage=4 立刻停发并补 Stage=2。§5.2 图只有 Speaking → FinishingUpload（主动发 Stage=2）。补：Speaking + Stage=4 → FinishingUpload。

### N5. `POST /wait` 缺少作用域

多设备会串台。强制 `device_id`，建议同时支持 `turn_id`。

### N6. 语言选型口径不一致

`architecture.md` §7：未通过收包冒烟前不得写死默认语言；推荐 Go。  
README：Phase 1 Core 推荐 Go，读起来像已锁定。

改成「推荐 Go（gorilla 同类）；收包冒烟通过后锁定」。

---

## 5. 验证缺口

| ID | 缺口 | 影响 |
|----|------|------|
| G1 | 本次仍未对真实 core 做 Go/Python 拨号 | B1 收包结论来自协议与库契约。Phase 1 开工前用最小客户端收一帧 `'0'` TTS。 |
| G2 | commit 未填 | 与阻塞 B2 同一件事。`bad_seq` 期望无法判定。 |
| G3 | 本仓库没有 `testdata/golden_frames` 或示例音频 | 对规划审查可接受。实现第一周从钉死的 `AudioHeader` 生成并检入；禁止从 mock.go 导出。 |

---

## 6. 建议改入各文件的条文草案

### 6.1 `README.md`

删除「关闭所有 P1 阻塞项」。改为：

> 已吸收 v1 审查的多数条款。剩余阻塞项见 `plan-review.md`。未填对齐基线、未写注入通道前，不得开始 Phase 1 实现。

### 6.2 `architecture.md` §4.2（注入 vs 本地校验）

```text
local_validation_error
  正常路径上发现非法配置或未注入的坏参数，帧不得发出。

expected_server_drop（主类型；inferred_no_reply 为别名）
  帧已写入连接，timeout 内无匹配 UUID 的 ASR/TTS。
  注入路径必须走这里。

injected_fault
  元数据，不是独立成功标准：skip_register | skip_report | bad_seq | oversize | bad_header | ...

注入路径允许在 Connection 尚未 Ready 时发送音频。
本地 max_payload / format / Seq 守卫对注入帧不生效。
```

### 6.3 `architecture.md` §10

实现前由负责人填写，例如：

```text
目标仓库路径：C:\Users\xie_f\projects\go\ai-creates-wealth
commit / tag：<40 位 SHA 或 tag>
是否包含 MH 前缀 Seq 例外：是 / 否
非 MH + 无活跃 turn + Seq>0 的期望：丢弃 | 开新 turn
本地 core：ws://127.0.0.1:8089/
```

（上表路径仅为示例，以实际要对齐的树为准。）

### 6.4 `phase1.md` §5.2

在「仅在 Connection == Ready 后可启动」后增加例外：

```text
注入路径例外：skip_register / skip_report / 任意 injected_fault
允许在 Connected 之后启动 UplinkTurn 或直接写帧，不要求 Ready。
```

状态图补：

```text
Speaking + 收到 Stage=4 → FinishingUpload（停发 Stage=1，补 Stage=2）
```

### 6.5 `phase1.md` §7

- `downlink_ack`：改名为 `expect_downlink_need_ack`，或注释「仅描述机型预期；客户端见 NeedAck=1 必须 ACK」。
- 注入项注明：outbound 必须出现在 `frames.jsonl`。

### 6.6 `phase1.md` §10

负向用例改写为：

- 注入 skip_register / skip_report / bad_seq / oversize 时，outbound 有对应帧。
- timeout 内出现 `expected_server_drop`，带同一 `injected_fault`。
- 不要求事件名等于服务端日志原因。
- `bad_seq` 的通过条件引用 §10 对齐基线里「丢弃 | 开新 turn」那一行；基线写开新 turn 则本条改为「服务端开新 turn，不得报 drop」。

### 6.7 `phase2.md` §5 / §6

- `POST /wait` 必填 `device_id`，可选 `turn_id`。
- 定义 `tts_done`（见 N2）。主断言保持 `tts_done` + `turn_end_reason: idle`。

---

## 7. 上一轮 P1 对照

| v1 | 主题 | v2 状态 |
|----|------|---------|
| B1 | Python 默认栈收不下二进制 Text 下行 | 已关闭。按字节收包；推荐 Go；验收含真实 TTS 一帧。 |
| B2 | golden 对齐 mock.go 与协议互斥 | 已关闭。权威源改为 `types.AudioHeader`；mock.go 偏差单另列。 |
| B3 | Phase 1 多进程 CLI 无 IPC | 已关闭。one-shot `speak`；长连接推迟 Phase 2。 |
| B4 | `MH-TOY` + 服务端未钉死 | 部分关闭。示例改为 `A3`、禁止 MH 前缀。commit 仍空 → 本轮 B2。 |
| B5 | 客户端假装发出服务端静默原因 | 已关闭。禁止 `device_not_found` 等；14007→`protocol_error`；5001→`ack_failure`。 |

v1 非阻塞项在 v2 中的去向：`finish_reason` 已拆分；打断换 UUID 已写；keepalive 指定 report；ACK 骨架已写清；Scenario ASR 已 optional；联调前置已列出。剩余见本轮 N1–N6。

---

## 8. 明确不在本次审查范围

- 未实现模拟器代码（v2 目录仍是文档）。
- 未把本审查条款写回 `architecture.md` / `phase1.md`（需另开修改任务）。
- MQTT、拍照 `'2'`、转发 `'3'`。
- 对 Phase 3 视觉稿、Phase 4 指标的产品评价。

---

## 9. 建议下一步

1. 按第 2、第 6 节改 v2 的 `README.md`、`architecture.md`、`phase1.md`（必要时 `phase2.md`）。
2. 填实对齐 commit，关闭 B2 / G2。
3. 用最小 Go 客户端对 `ws://127.0.0.1:8089` 收一帧 TTS，关闭 G1。
4. 再开 Phase 1 实现；注入用例必须在 `frames.jsonl` 里看得到出站坏帧。
