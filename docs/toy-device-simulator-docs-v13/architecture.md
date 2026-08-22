# 玩具设备模拟器 — 整体架构设计文档（v13）

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：

1. **忠实模拟** 真实玩具设备的 chatbot 协议行为
2. **批量设备并发测试**（模板、错峰、限额）
3. 参数可配置、可保存
4. **Agent 可编程 API**（REST + 事件 WS）
5. **人工调试 UI**（配置 + 简单对话）

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。主路径：`Action=chatbot`。

**每阶段做完即可交付使用。** Phase 1 的交付物是能跑通的单设备 CLI，不是一份竞态备忘录。

## 2. 设计原则

1. **协议忠实**：对齐协议与基线 `types.AudioHeader`。`example/asr/mock.go` 仅缺陷清单，禁止作 golden。
2. **失败按 fault 矩阵推断**，禁止伪造服务端日志原因名。
3. **Turn 一等公民**：拆分 `uplink_end_reason` / `turn_end_reason`；先写不改。
4. speak 受理时 CAS Reserved。
5. **API 先于 UI**。
6. Phase 1/2 线上 **pcm**（mono s16le）。WAV 只作源文件。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK **只看报文** `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变。
10. 推荐实现语言 **Go**（按字节收 TextMessage 中的二进制）。

已关闭且必须保留：提前下行只缓存、report 锁内取号后解锁再 enqueue、register 先登记 timer 再发送且一次性消费、仅 IsFinal 可 silent、writePump、限额 generation + once、`/wait` 与终态同锁、统一 `turn_terminal`。

本版补：finalizer **三段**（permit 在关 socket 之后）、正常停机事件、`evicted_through_seq`、完整完成矩阵（本目录写全）、REST 媒体走 `asset_id`。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 生命周期 / 模板批量 / stagger
                conn_permit + speak_permit
                conn_generation + request_finalize 三段
DeviceInstance:
  writePump（唯一出站；有界队列）
  读循环（不阻塞落盘）
  keepalive report + last_activity
  pending_reports / early_downlink_buf / event_log
  Connection + UplinkTurn + DownlinkPlayer
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{turn_id}/
assets/{asset_id}                 # 仅 Phase 2 HTTP 上传根
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid
├── state     # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── injected_fault
├── uplink_end_reason   # 空 | stage2 | interrupt | vad | error | timeout；非空不改
├── turn_end_reason     # idle | interrupt | error | timeout | connection_lost
├── reply_kind          # 空 | tts | command | json | silent | command+tts | json+tts
├── related / early_downlink_buf
├── first_reply_timer / settle_timer
├── chunks / asr_results[] / commands[] / json_replies[] / events[]
```

- 每设备一槽。speak / speak_and_wait 受理时 CAS Reserved。speak_and_wait 与 completion waiter **同锁注册**。
- 默认 reject → 409。禁止用「第一帧是否已发」判断占用。
- **WaitingReply 之前**禁止因 command / 成功 JSON / 匹配 TTS 将 Turn 置 Terminal。

`uplink_end_reason`：Stage=4 立即 `vad`（若空）再补 Stage=2；已非空则后续 Stage=2/取消不得覆盖。Reserved 取消：保持空。连接失败且套接字已死：若空则 `error`。

**进入 Terminal 的唯一路径**（持 **device_mu**，与 `/wait` 注册同一把锁；**不得**同时持 `conn_mu` 等待 IO）：

1. 写入 Turn 快照（state=Terminal、`turn_end_reason`、`uplink_end_reason`、`reply_kind`）。
2. 释放 speak_permit（该 turn_id 的 once，见 §4.14）。
3. 释放槽。
4. **append `turn_terminal` 到 event_log**（每次 Terminal 都必须发，不论有无 TTS）。
5. 复制 waiter 列表。
6. **解锁后再唤醒**（log 必须已对同锁读取可见）。

禁止先唤醒再写 log。禁止 Terminal 而不发 `turn_terminal`。

### 4.2 Event

每条事件有 `device_id` 与单调 `event_seq`（从 **1**）。连接级用 `correlation_id`。Reserved 之后的对话事件用 `turn_id`。

禁止声称所有事件都有 `turn_id`。禁止发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志名。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
| `ack_failure` | `'1'` + `/register/client` 且 `data.code != 0`；随后 `request_finalize(register_nack)` |
| `report_echo` | `/report/client` 的 `ReportData` 命中 `pending_reports[seq]` |
| `report_echo_unmatched` | 回显序号不在表中 |
| `report_timeout` | pending 项超时 |
| `connection_failed` | **仅异常收口**。Phase C 已 Stopped 且 `Connection=Disconnected` 之后写入；带 `reason` |
| `connection_stopped` | **仅用户 stop**（`user_stop`）。不设 `last_error` |
| `device_deleted` | **仅用户 delete** 从 Manager 摘键之后。不设 `last_error` |
| `asr_result` | 无前缀 JSON `Action=asr_result`（含 interim；完成判定只认 IsFinal） |
| `command_received` | `'1'` + `/command/client` |
| `json_reply` | 无前缀 `Code==0` 且非 asr_result |
| `tts_chunk` | 匹配 UUID 的 `'0'` Stage=1 |
| `tts_done` | ≥1 帧匹配 TTS，且本次经 TTS idle 进入 Terminal |
| `vad` | 收到 Stage=4 |
| `expected_server_drop` | WaitingReply 首包到期；无终态相关下行；无匹配且 **IsFinal=true** 的 asr_result。仅矩阵 drop 行作为通过 |
| `protocol_error` | `Code=1` / `14007` 或非法首字节 |
| `early_downlink_overflow` | 提前下行缓冲溢出 |
| `turn_terminal` | **槽已释放。** 必带 `turn_id`、`turn_end_reason`、`uplink_end_reason`、`reply_kind`。command/JSON/silent/interrupt/timeout/error/connection_lost **一律发送** |

`inferred_no_reply` 是 `expected_server_drop` 的别名，实现只保留一个主类型。

**event_log 与游标**

- 保留最近 **10000** 条或 **24h**（先到为准）。
- 显式维护 `evicted_through_seq`：已丢弃的最大 `event_seq`。空日志且从未淘汰时为 **0**。淘汰后恒有 `oldest_seq == evicted_through_seq + 1`（若仍有条目）。
- `after_event_seq` 为 **排他**游标：返回/等待 `event_seq > after_event_seq`。
- **410 `event_seq_expired`** 当且仅当 `after_event_seq < evicted_through_seq`。body：`evicted_through_seq`、`oldest_seq`、`newest_seq`。只用 410，不用 409。
- 合法边界：`after_event_seq == 0` 且 `evicted_through_seq == 0`（oldest=1）→ **不**过期。`after_event_seq == oldest_seq - 1` → **不**过期。`after_event_seq == oldest_seq - 2` 且已有淘汰 → **410**。
- 省略 `after_event_seq`：只等未来事件，不回放，不 410。

### 4.3 `/wait` 与 speak_and_wait 原子窗

**device_mu** 同时保护：Turn 状态、event_log 追加、waiter 集合。

`POST /wait` 持该锁：

1. 校验 `device_id`；缺则 400。
2. 若有 `turn_id`：无此 Turn → 404；已 Terminal → **立即 200**（读已落库快照）；仍活动 → **同一临界区登记 waiter**，再解锁阻塞。
3. 若按 `event_type`：`after_event_seq < evicted_through_seq` → 410；log 中已有 `seq > after` 的匹配 → 立即 200；否则同一临界区登记 waiter。
4. 禁止「读完状态、解锁、再注册」。

`speak_and_wait`：CAS Reserved + 登记 completion waiter **同一临界区**（成功后再 enqueue 音频）。超时/客户端断开：只摘本 waiter，不 Terminal。

### 4.4 相关下行、提前缓存、完成矩阵

基线可不下发 TTS：`SyncDevice` 纯 command；`ReplyModeText` 成功 JSON；空 `replyText` 无包。

**关联**

| 种类 | 形态 | 本轮判定 |
|------|------|----------|
| TTS | `'0'` | UUID == uplink_uuid |
| command | `'1'` `/command/client` | 无 UUID；Turn 已 Speaking（发过 `'0'`）或之后 |
| asr_result | `{` Action=asr_result | SessionID == uuid 十进制；记录 IsFinal |
| 成功 JSON | `{` Code==0，非 asr_result | 同 command 占用 |
| 失败 JSON | Code=1 / 14007 | 占用；立即停上行 |

**首次收到时（含 WaitingReply 前）：** 发事件、按需 ACK、录帧、落盘。写入 `early_downlink_buf`（若尚未 WaitingReply）。容量默认 **32** 条；溢出丢最旧并 `early_downlink_overflow`。

**WaitingReply 前：** 不启动完成计时、不 Terminal、不释放槽。失败 JSON 除外（取消表停上行）。发送侧每帧 Stage=1 前检查 `state != Terminal`。

**进入 WaitingReply 后回放缓冲：只驱动计时状态**（取消 `first_reply_timer`、启动 settle 等）。**禁止**第二次 ACK、第二次事件、第二次录帧、第二次音频落盘。然后清空缓冲。

**计时（仅 WaitingReply 起）**

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态下行取消；默认 20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| 非音频 followup | command/成功 JSON 且尚无 TTS | 默认 5s；其后有 TTS 则改 idle |
| post_final_asr_silence | first_reply 到期 **且** 已有 **IsFinal=true** 且无终态下行 | 默认 5s → silent idle。interim-only **不得**启动 |

**终止（进入 Terminal 后必发 `turn_terminal`）：**

| 路径 | 计时 | `reply_kind` | 额外事件 |
|------|------|----------------|----------|
| 仅 TTS | TTS idle | `tts` | `tts_done` |
| 仅 command | followup 后 idle | `command` | 无 `tts_done` |
| 仅成功 JSON | followup 后 idle | `json` | 无 `tts_done` |
| command 后有 TTS | 改 TTS idle | `command+tts` | `tts_done` |
| JSON 后有 TTS | 改 TTS idle | `json+tts` | `tts_done` |
| 仅 IsFinal、无终态下行 | silent idle | `silent` | 无 `tts_done` |
| 仅 interim 或全无 | timeout | 空 | drop 行另发 `expected_server_drop` |
| 失败 JSON | 立即 | 空 | `protocol_error`；`turn_end_reason=error` |

完全无包的静默成功与 drop 不可区分：正常路径 timeout。探针 Phase 4。

等待预算：`upload + first_reply + max(idle, followup) + slack`。禁止默认写死 30s。

### 4.5 Scenario 断言

TTS 用例：`tts_done` + idle，并等 `turn_terminal`。纯指令：`command_received` + `turn_terminal`，**不要** assert `tts_done`。文本：`json_reply`。`asr_contains` 仅 `StreamingAsrTextReply`，optional。

### 4.6 注入矩阵

不查 Mongo。`skip_register`：夹具 `sim_sr_{run_uuid}_{n}`，本趟不预注册，写 `fresh_ids.jsonl`，**新建**实例。

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1；服务端无 ASR session | drop |
| oversize | payload>51200 | drop |
| bad_header | `'0'` + 其后 **少于 100 字节** | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

### 4.7 取消表

套接字已死或 Connection 已是 Disconnecting/Disconnected 则跳过出站。`uplink_end_reason` 非空不改写。

| 状态 | 出站 | 空的 uplink_end_reason | turn_end_reason |
|------|------|------------------------|-----------------|
| Reserved | 不发 Stage=3 | 保持空 | interrupt / connection_lost |
| Speaking | Stage=3；停 Stage=1 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；可 Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | 可 Stage=3 | 保持 | 同上 |
| WaitingReply | 可 Stage=3；取消计时器 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

用户 interrupt → `turn_end_reason=interrupt`。连接收口 → `connection_lost`。失败 JSON/本地错误 → `error`。

### 4.8 report 取号

`report_next_seq` 初值 = `report_sequence_start`（默认 1）。**第一个序号等于 start。**

同一把 `report_mu`：取号、递增、登记 `pending_reports[seq]`。**必须先解锁 `report_mu`，再 writePump Enqueue。** 禁止持 `report_mu` 做 IO。可用 `atomic.AddUint64`（初值 start-1）+ 锁/`sync.Map` 插入 pending。禁止无锁读改写、禁止无锁 Go map。

仅 `kind=initial` 且 Reporting → Ready。initial 超时 → `request_finalize(report_timeout)`。keepalive 与手动 report 共用此路径。

### 4.9 writePump 与配置所有权

每个 DeviceInstance **唯一**出站入口。必须经此：register、report（含 keepalive）、音频 Stage=1/2/3、binary/JSON ACK、其它管理帧。

同一 Turn 的 Stage=1 各片与最后 Stage=2 **按入队顺序写出**（由单一上行协程按序 Enqueue）。

**队列**

| 项 | 默认 | 所有权 |
|----|------|--------|
| `write_queue_depth` | 256 | **Phase 1**：设备 YAML `behavior`。**Phase 2**：只读 Manager YAML；设备 YAML **禁止**出现这两项，出现则创建/PUT **400** |
| `write_drain_timeout_sec` | 2 | 同上 |

- 队列满：Enqueue 失败 → `request_finalize(write_backpressure)`；若由 HTTP 触发且尚未对调用方成功响应，HTTP **503**。
- 写失败 → `request_finalize(write)`。
- writePump 协程 **禁止**回锁 `manager_mu` / `device_mu` / `conn_mu`。

### 4.10 ACK

运行时只看报文标志。Phase 1：音频+指令 binary，SleepMs=0。Phase 2：json、非零 SleepMs、节流 A/B/C。

`sleep_ms: 0` 不写节流缓存、不能清旧值。正值用例必须不同 `device_id`。

**音频 binary：** Ack=音频 Seq；DownlinkType TTS=1 / 提示音=2；Code/SleepMs=配置。  
**音频 JSON：** ack 与 sequence_number=音频 Seq；downlink_type=`tts`/`hint_audio`；uuid=音频 UUID；sleep_ms/code=配置。  
**指令 binary：** Ack=指令 sequence_number（缺省 0）；DownlinkType=**3**。  
**指令 JSON：** ack 与 sequence_number=指令序号；downlink_type=`command`；topic=原指令完整 topic；uuid 省略或 0；sleep_ms/code=配置。

binary：`'4'`+28 字节。json：`'1'` + `.../downlink-ack/server`。

A/B/C：**同一 device_type** 且 DownlinkAck=true；三台不同 ID；status=1；TTS≥2 片。A=binary/0，B=binary/500，C=json/500。

### 4.11 身份与 playingMode

`device_id` 创建后不可变。PUT/faults 改 ID → 400。skip_register 用夹具 ID 新建。

**热更新：** `playingMode` 可通过 `POST /report` 在 **Ready** 时热更，keepalive report 带当前值。  
**必须重连：** enterprise、device_type、device_id。Running 时改这些 → 409。

### 4.12 锁顺序、register 一次性消费、Running 映射、三段 finalizer

**锁顺序（禁止反转）：** `manager_mu` → `device_mu` → `conn_mu` → `report_mu`。  
**禁止**持上述任一把锁做 drain、close、写盘、或调用 `request_finalize`。  
Phase 1 无 Manager：省略 `manager_mu`，其余相同。

**每次成功 occupy 进入 Starting：** `conn_generation++`；`permit_held=true`（仅 Phase 2）。Registering 另分配 `register_attempt_id`。

**发送 register：**

1. 持 `conn_mu`：Connection=`Registering`；保存 `(conn_generation, register_attempt_id)`；`register_settle=pending`；启动 timer。
2. **解锁。**
3. writePump 发送。
4. `skip_register` 不发送、不装 timer，Connection 停 Connected。

**一次性消费 `try_consume_register_settle()`**（必须持 `conn_mu`）：CAS pending→acked 或 pending→timeouted。只有赢的一方推进。

**ACK 路径（禁止持锁调 finalizer）：**

1. 持 `conn_mu`；generation/attempt 不匹配则解锁返回。
2. `try_consume` 失败则解锁返回（timeout 已赢）。
3. `Timer.Stop()`（不等待 callback）。
4. `code==0`：Connection=`Registered`；解锁返回。
5. `code!=0`：记下 `ack_failure` 所需字段、`gen`；**解锁**；append `ack_failure`（可再取 `device_mu`）；`request_finalize(gen, register_nack)`。

**timer callback：** 持 `conn_mu`；generation/attempt 不匹配或 Connection≠Registering → 解锁返回；`try_consume` 失败 → 解锁返回；成功则记下 `gen`，**解锁**，再 `request_finalize(gen, register_timeout)`。  
**禁止**依赖 `Timer.Stop` 取消已开始的 callback。

**实例 Running：**

| 路径 | Connection | Starting→Running | speak 前置 |
|------|------------|------------------|------------|
| 正常 | Ready | Ready 达成时 | Running 且 Ready |
| skip_register | 停在 Connected | Connected 达成时 | Running 且 Connected |
| skip_report | 停在 Registered | Registered 达成时 | Running 且 Registered |

GET 同时返回 `instance_state` 与 `connection_state`。禁止注入路径永远停在 Starting。

**`request_finalize(generation, reason)` — 唯一退出入口**  
调用方 **不得**持 `conn_mu`/`device_mu`/`manager_mu`。stop、delete、握手失败、ACK nack、timeout、读写失败、回压均走这里。幂等：同一 generation 只执行一次。

`reason`：

| reason | 事件 | `last_error` |
|--------|------|----------------|
| `user_stop` | `connection_stopped` | 不设置（清空） |
| `user_delete` | 若本趟实际关了连接则先 `connection_stopped`；摘键后 `device_deleted` | 不设置 |
| `handshake` / `register_timeout` / `register_nack` / `read` / `write` / `write_backpressure` / `remote_close` / `report_send` / `report_timeout` | `connection_failed`（带 reason） | 设为该 reason |

**Phase A — 锁内声明（无 IO）**

按锁顺序加锁：

1. `conn_generation != generation` → 解锁 return。
2. 该 generation 已 `finalize_started` 或 `finalize_committed` → 解锁 return。
3. `finalize_started=true`；实例 → **Stopping**（已 Starting/Running 时）；Connection → **Disconnecting**。
4. 取消全部 timer（Stop，不等待）。
5. 若 Turn 未 Terminal：按取消表计算终态字段；若需 Stage=3 且 socket 仍可用，记下 `need_stage3`（**不在锁内发送**）。
6. 快照 websocket 与 writePump 句柄。
7. 解锁。

此时 **不**释放 `conn_permit`。start 见到 Stopping → **409**（不是 429），因此 drain 期间不会突破 `max_connections`。

**Phase B — 锁外 IO**

1. 若 `need_stage3`：Enqueue Stage=3（失败忽略，继续关）。
2. 停止接收新入队；drain 已入队帧，时限 `write_drain_timeout_sec`；超时丢剩余。
3. 关闭 websocket。
4. 等待读循环退出（同一时限）。

**Phase C — 锁内提交（无 IO）**

按锁顺序加锁：

1. generation 不匹配或已 `finalize_committed` → 解锁 return。
2. `finalize_committed=true`。
3. Connection → **Disconnected**。
4. 若 Turn 仍未 Terminal：按取消表（socket 已死，跳过出站）走 §4.1 终态（含 `turn_terminal`、speak_permit once）。
5. 清空 pending_reports。
6. 若 `permit_held`：释放 **一个** conn_permit；`permit_held=false`。
7. 实例 → **Stopped**。仅异常 reason 写 `last_error`；`user_stop`/`user_delete` 清空 `last_error`。
8. append `connection_failed` 或 `connection_stopped`（见上表）。`device_deleted` 只在 delete 摘键后、本函数返回之后由 Manager 追加。
9. 复制 waiter；解锁后唤醒。

delete：若仍 Starting/Running/Stopping，调用 `request_finalize(gen, user_delete)` **等 Phase C 完成**再摘键；已 Stopped 则不调 finalizer、不释放 permit，只摘键并 `device_deleted`。

### 4.13 keepalive 与 last_activity

- `keepalive_interval_sec` 默认 **60**，方法为周期 **report**（避开 register 每秒限流）。
- 仅 Ready 之后启动（skip_report 不停 Ready，不发 keepalive）。
- 暴露 `last_activity`（任意入站或成功出站刷新）。
- 服务端约 **360s** 空闲踢线：有 keepalive 时连接应保持。

### 4.14 读循环不阻塞落盘

独立读协程持续读全部下行。指令 / ASR / TTS 头 **立即**进事件总线。帧录制、PCM 落盘、解码放工作队列，**禁止**在读协程里同步写盘。

### 4.15 限额闸门

错误码：**限额 429**；状态冲突 **409**。不用 409 表示限额。

**conn_permit**（同一把 manager+device 锁）：

- 若状态不是 Created/Stopped → **409，不 TryAcquire**（含 Stopping：旧连接仍占额度）。
- TryAcquire 失败 → **429**，状态不变。
- 成功：`permit_held=true`；`conn_generation++`；Starting。
- 释放 **只**在 Phase C 且 `permit_held`。

**speak_permit：** CAS Reserved 成功路径内 TryAcquire；失败则 429 且不留下 Reserved。CAS 失败则未 Acquire。§4.1 Terminal 对该 turn_id once 释放。

并发测试必须覆盖：重复 start、超限 start、stop∥delete、stop∥断线、ACK∥timeout、finalizer 重入、drain 期间再 start（须 409 且连接数不涨）。

批量创建冲突 **整批 409**。`POST /devices/batch/start|stop|delete`。`default_stagger_ms` 默认 50。

### 4.16 REST 媒体（仅 Phase 2）

禁止 HTTP 接受任意服务器本地路径。

- 上传：`POST /assets` multipart，写入 `assets_root`（默认 `./data/assets`）。拒绝 `..`、绝对路径、符号链接逃逸。
- 限制：`max_asset_bytes=10485760`（10MiB）；WAV 解出 PCM 后 `max_asset_duration_sec=60`。超限 400。
- speak 只引用返回的 `asset_id`（或 stream 里的 `asset_id` + silence）。
- 播放：`GET /devices/{id}/turns/{turn_id}/audio/downlink`（及 uplink）。
- Phase 1 CLI `--audio` 仍读本机文件，不经过 HTTP。

## 5. 协议硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text 帧，禁止 UTF-8 解码下行音频 |
| AudioHeader | 100 字节含 padding；golden 对照基线；禁止 mock.go |
| 占用 | 受理时 CAS Reserved |
| 新一轮 | Seq=0 + Stage=1；禁止 MH 前缀机型作示例 |
| Stage=4 | 立即记 vad（若空），停 Stage=1，补 Stage=2 |
| 下行结束 | 无稳定 Stage=2；完成矩阵见 §4.4 |
| **心跳** | 周期 report；`last_activity`；防 360s 空闲踢线 |
| **读循环** | 持续读；指令立即进事件总线；**不因写文件/解码阻塞** |
| **热更新** | playingMode 经 report；身份字段必须重连 |
| Register | 先登记超时再发送；一次性消费；持锁时不调 finalizer |
| Report | 锁内取号；解锁后再 enqueue；首序号=start；匹配回显才 Ready |
| 线上格式 | pcm s16le mono |
| ACK | 只看报文标志 |
| 出站 | 全部走 writePump |
| 收口 | 三段 finalizer；Disconnected；permit 在 close 之后 |
| 配置 | Device 三段、playing_mode ∈ {1,2,3}、UUID 范围 |

## 6. 音频管线

源 WAV 解 RIFF → 内部 PCM → silence=全零采样 → 按 slice_ms 切 **pcm 字节**。禁止逐片封装 WAV。Phase 4 才允许线上 mp3/wav（整段只编码一次）。

## 7. 技术选型

硬门槛：按字节收 TextMessage 二进制。Phase 1 **推荐 Go**。真实 core 收到至少一帧 `'0'` TTS 且未因 UTF-8 失败后锁定。

## 8. 阶段（边界不变）

| 阶段 | 交付定义（做完就能用） |
|------|------------------------|
| Phase 1 | 单设备：握手→register→report→pcm 上行→收下一条回复；CLI；落盘；keepalive |
| Phase 2 | 批量+API+Scenario；playingMode 热更新；JSON ACK；资产上传与录音下载 |
| Phase 3 | Web UI 只消费 Phase 2 |
| Phase 4 | queue、非 pcm、静默探针 |

## 9. 目录

`protocol/` `core/`（writePump、request_finalize、pending_reports、early_downlink、event_log）`cmd/speak|check|fixture/` `configs/example_device.yaml` `configs/manager.yaml` `configs/templates/` `testdata/` `data/assets/`

## 10. 对齐基线

仓库 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。  
register Redis 出错可不 ACK。管理消息独立 goroutine。MH Seq 例外。不要用 `projects/go/ai-creates-wealth`。
