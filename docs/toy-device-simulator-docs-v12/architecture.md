# 玩具设备模拟器 — 整体架构设计文档（v12）

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：

1. 忠实模拟真实玩具设备的 chatbot 协议
2. 批量设备并发测试（模板、错峰、限额）
3. 参数可配置、可保存
4. Agent 可编程 API（REST + 事件 WS）
5. 人工调试 UI（配置 + 简单对话）

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。主路径：`Action=chatbot`。

**每阶段做完即可交付。** Phase 1 交付物是能跑通的单设备 CLI。

## 2. 设计原则

1. 协议忠实；mock.go 仅缺陷清单，禁止作 golden。
2. 失败按 fault 矩阵推断；禁止伪造服务端日志原因名。
3. Turn 一等公民；`uplink_end_reason` 先写不改。
4. speak 受理时 CAS Reserved。
5. API 先于 UI。
6. Phase 1/2 线上 pcm（mono s16le）。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK 只看报文 `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变。
10. 推荐 Go。

保留纠错：提前下行只缓存、report 锁内取号、register 先登记再发送、仅 IsFinal silent、writePump、限额闸门。本版补：permit **generation + 幂等 finalizer**、register **一次性消费**、`/wait` 与终态同临界区、统一 `turn_terminal`。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 模板批量 / stagger / conn_permit+speak_permit
                conn_generation + 幂等 connection_finalizer
DeviceInstance:
  writePump（唯一出站；有界队列）
  读循环（不阻塞落盘）
  keepalive report + last_activity
  pending_reports / early_downlink_buf / event_log
  Connection + UplinkTurn + DownlinkPlayer
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn、占用、终态落库

状态：Reserved | Speaking | FinishingUpload | WaitingReply | Terminal。

- 每设备一槽。speak / speak_and_wait 受理时 CAS Reserved。
- **WaitingReply 之前**禁止因 command / 成功 JSON / 匹配 TTS 将 Turn 置 Terminal。
- `uplink_end_reason`：Stage=4 立即 `vad`（若空）再补 Stage=2；非空不改。Reserved 取消保持空。

**进入 Terminal 的唯一路径**（持 **设备锁**，与 `/wait` 注册同一把锁）：

1. 写入 Turn 快照（state=Terminal、`turn_end_reason`、`uplink_end_reason`、`reply_kind`）。
2. 释放 speak_permit（该 Turn 的 once，见 §4.14）。
3. 释放槽。
4. **append `turn_terminal` 到 event_log**（每次 Terminal 都必须发，不论有无 TTS）。
5. 复制 waiter 列表。
6. 解锁后（或仍持锁但 log 已可见）唤醒全部 waiter。

禁止先唤醒再写 log。禁止 Terminal 而不发 `turn_terminal`。

### 4.2 Event

每条事件有 `device_id` 与单调 `event_seq`（从 1）。连接级用 `correlation_id`。Reserved 之后的对话事件用 `turn_id`。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
| `ack_failure` | `/register/client` 且 `data.code != 0` |
| `report_echo` / `report_echo_unmatched` / `report_timeout` | report 回显匹配 / 未匹配 / pending 超时 |
| `connection_failed` | **connection_finalizer 已把实例置 Stopped 之后**写入 |
| `asr_result` | `Action=asr_result`（完成判定只认 IsFinal=true） |
| `command_received` | `'1'` + `/command/client` |
| `json_reply` | 无前缀 `Code==0` 且非 asr_result |
| `tts_chunk` / `tts_done` | 匹配 UUID 的 TTS 分片 / TTS idle 完成（仅有 TTS 时） |
| `vad` | Stage=4 |
| `expected_server_drop` | 首包到期且无终态下行、无 IsFinal；仅矩阵 drop 行作为通过 |
| `protocol_error` | `Code=1` / `14007` 或非法首字节 |
| `early_downlink_overflow` | 提前下行缓冲溢出 |
| **`turn_terminal`** | **槽已释放。** 必带 `turn_id`、`turn_end_reason`、`uplink_end_reason`、`reply_kind`。command/JSON/silent/interrupt/timeout/error/connection_lost **一律发送** |

event_log：最近 10000 条或 24h。`after_event_seq` < oldest → **410** `event_seq_expired`（只用 410，不用 409），body：`oldest_seq`、`newest_seq`。

### 4.3 `/wait` 与 speak_and_wait 原子窗

**设备锁**同时保护：Turn 状态、event_log 追加、waiter 集合。

`POST /wait` 持锁：

1. 校验 `device_id`；缺则 400。
2. 若有 `turn_id`：无此 Turn → 404；已 Terminal → **立即 200**（读已落库快照 + `turn_terminal`）；仍活动 → **在同一临界区登记 waiter**，然后释放锁阻塞。
3. 若按 `event_type`：游标过期 → 410；log 中已有匹配 → 立即 200；否则同一临界区登记 waiter。
4. 禁止「读完状态、解锁、再注册」。

`speak_and_wait`：CAS Reserved + 登记 completion waiter **同一临界区**（成功后再 writePump 发音频）。

超时/客户端断开：只摘本 waiter，不 Terminal。

### 4.4 相关下行、提前缓存、终止矩阵

基线可不下发 TTS。关联、缓冲、仅 IsFinal 可 silent、WaitingReply 前回放只驱动计时：规则同前一版实施契约，摘要如下。

- TTS：UUID 匹配。command/成功 JSON：占用关联。asr_result：SessionID。失败 JSON：立即停上行。
- 首次收到：事件、ACK、录帧、落盘；未 WaitingReply 则入 `early_downlink_buf`（容量 32，溢出丢最旧）。
- WaitingReply 前不因 command/JSON/TTS 而 Terminal。
- 回放：**只改计时器**，禁止二次 ACK/事件/录帧/落盘。
- 终止后 **必须**走 §4.1 的 `turn_terminal`。`tts_done` 仅 TTS idle 路径额外发送。

等待预算：`upload + first_reply + max(idle, followup) + slack`。

### 4.5–4.6 Scenario 与注入、取消表

Scenario：TTS 用 `tts_done`；纯指令用 `command_received`；文本用 `json_reply`；槽释放用 `turn_terminal`。

注入矩阵：skip_register 夹具新建；skip_report 不默认 drop；bad_seq/oversize/bad_header 为 drop；dup_uuid/bad_stage 不作为 drop 验收。

取消表：Reserved 不发 Stage=3 且 uplink_end_reason 保持空；其余可 Stage=3；非空 uplink_end_reason 不改写。

### 4.7 report 取号

`report_next_seq` 初值 = `report_sequence_start`。首个序号 = start。

`report_mu` 内：取号、递增、登记 pending。**必须先解锁 `report_mu`，再 writePump Enqueue。** 禁止持 `report_mu` 做 IO。

### 4.8 writePump

唯一出站入口。容量 `write_queue_depth`（默认 **256**）。

- Enqueue 成功：按序写出。同一 Turn 的 Stage=1 各片与 Stage=2 由单一上行协程按序入队。
- 队列满：Enqueue 失败 → `fail_connection(write_backpressure)`；若由 HTTP 触发且尚未对调用方成功响应，HTTP **503**，并走 finalizer。
- 关闭：停止接收新入队；**drain** 已入队帧（时限 `write_drain_timeout_sec` 默认 2）或丢弃剩余；然后关 socket。
- 写失败 → connection_finalizer(`write`)。

覆盖：register、report/keepalive、Stage=1/2/3、ACK。

### 4.9 ACK

Phase 1：音频+指令 binary，SleepMs=0。Phase 2：json、非零 SleepMs、A/B/C。只看报文标志。

音频 binary：Ack=音频 Seq；DownlinkType 1/2。指令 binary：Ack=指令序号；DownlinkType=3。  
JSON 音频：ack/sequence_number=音频 Seq；uuid=音频 UUID。JSON 指令：ack/sequence_number=指令序号；topic=原指令 topic；uuid 省略或 0。

A/B/C：同一 **device_type** 且 DownlinkAck=true；三台不同 ID；≥2 片 TTS。

### 4.10 身份与 playingMode

device_id 不可变。playingMode 经 report 热更。enterprise/device_type/device_id 须重连。

### 4.11 register 一次性消费、Running 映射、connection_finalizer

**每次进入 Starting 成功 occupy 后：** `conn_generation++`（该次连接的 id）。Registering 另分配 `register_attempt_id`。

**发送 register：**

1. 持 `conn_mu`：Connection=Registering；保存 `(conn_generation, register_attempt_id)`；`register_settle` 置 pending；启动 timer。
2. 解锁。
3. writePump 发送。
4. `skip_register`：不发送、不装 timer。

**一次性消费 `try_consume_register_settle()`**（必须持 `conn_mu`）：CAS pending→acked 或 pending→timeouted。只有赢的一方推进状态。

**ACK 路径：** 持锁；generation/attempt 不匹配则忽略；`try_consume` 失败则忽略（timeout 已赢）；成功则 Stop timer（不等待 callback 结束）、Registered 或 nack→finalizer。

**timer callback：** 持 `conn_mu`；若 `conn_generation` 不匹配 **或** `register_attempt_id` 不匹配 **或** Connection≠Registering → **return**；`try_consume` 失败 → return；成功 → finalizer(`register_timeout`)。  
**禁止**依赖 `Timer.Stop` 取消已开始的 callback。

**实例 Running：**

| 路径 | Connection | Starting→Running | speak 前置 |
|------|------------|------------------|------------|
| 正常 | Ready | Ready 时 | Running 且 Ready |
| skip_register | Connected | Connected 时 | Running 且 Connected |
| skip_report | Registered | Registered 时 | Running 且 Registered |

**connection_finalizer(generation, reason)** — stop、delete、fail_connection、握手失败 **唯一**退出入口：

持锁：

1. 若 `conn_generation != generation` → return（过期 callback）。
2. 若该 generation 已 `finalized` → return。
3. 标记 finalized。
4. 若 `permit_held`：释放 **一个** conn_permit；`permit_held=false`。
5. 取消 timer；Turn 走取消表并 `turn_terminal`；清空 pending；drain writePump；关 WS。
6. 实例 → **Stopped**。
7. append `connection_failed`。
8. 解锁后唤醒 waiter。

delete：若仍 Running/Starting，只调用 finalizer；已 Stopped 则 **不再释放 permit**，只删键。

### 4.12–4.13 keepalive 与读循环

keepalive：Ready 后每 60s report；`last_activity`；防 360s 踢线。  
读循环独立；指令立即进事件总线；落盘异步。

### 4.14 限额闸门（generation + once）

Manager 配置见 Phase 2 schema。错误码：**限额 429**；状态冲突 **409**。不用 409 表示限额。

**conn_permit**

同一把 Manager/设备锁：

- 若状态不是 Created/Stopped → **409，不 TryAcquire**（重复 start 失败者无 permit 可漏）。
- TryAcquire 失败 → **429**，状态不变。
- 成功：`permit_held=true`；`conn_generation++`；Starting。失败握手只对 **该 generation** 调 finalizer。

释放 **只**发生在 `connection_finalizer` 内且 `permit_held`（once）。

**speak_permit**

CAS Reserved 成功路径内 TryAcquire；失败则 429 且 **不**留下 Reserved。CAS 失败则未 Acquire。Terminal 路径（§4.1）对该 turn_id **once** 释放。连接 finalizer 若 Turn 仍活动：先 Terminal（释放 speak_permit）再关连接。

并发测试：重复 start、start 超限、stop∥delete、stop∥断线、ACK∥register timeout。

批量：创建冲突整批 409；`batch/start` 每台走上述 start 锁；stagger 默认 50ms。

## 5. 硬约束

收包按字节；头 100B；CAS Reserved；Stage=4 先 vad；心跳 report+last_activity；读循环不阻塞落盘；playingMode 热更/身份重连；register 先登记再发送且一次性消费；report 解锁后再 enqueue；writePump；pcm；ACK 只看报文。

## 6–7. 音频与选型

内部 PCM 切片。推荐 Go。

## 8. 阶段（边界不变）

| 阶段 | 交付 |
|------|------|
| Phase 1 | 单设备 CLI：握手→register→report→pcm 上行→回复；落盘；keepalive |
| Phase 2 | 批量+REST/WS+Scenario；playingMode 热更新；JSON ACK |
| Phase 3 | UI 只消费 Phase 2 |
| Phase 4 | queue、非 pcm、静默探针 |

## 9. 目录

`protocol/` `core/` `cmd/speak|check|fixture/` `configs/example_device.yaml` `configs/manager.yaml` `configs/templates/`

## 10. 对齐基线

`C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。不要用 `projects/go/ai-creates-wealth`。
