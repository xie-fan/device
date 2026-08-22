# 玩具设备模拟器 — 整体架构设计文档（v14）

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
6. Phase 1/2 线上 **pcm**（mono s16le）。WAV 只作源文件；HTTP 上传也只接受 WAV。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK **只看报文** `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变。
10. 推荐实现语言 **Go**（按字节收 TextMessage 中的二进制）。

已关闭且必须保留：提前下行只缓存、report 锁内取号后解锁再 enqueue、register 先登记 timer 再发送且一次性消费、仅 IsFinal 可 silent、writePump、限额 generation + once、`/wait` 与终态同锁、统一 `turn_terminal`、三段 finalizer、permit 在 close 之后、`evicted_through_seq`、正常停机事件。

本版补：finalizer **join-wait**（`finalize_done`）、`BeginClose(finalFrame)`、等待预算含 `post_final_asr_silence`、Scenario 捕获游标、HTTP 仅 WAV、WS 必填 `device_id`、身份字段仅 Created/Stopped 可改。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 生命周期 / 模板批量 / stagger
                conn_permit + speak_permit
                conn_generation + request_finalize（join-wait）
DeviceInstance:
  writePump.BeginClose(finalFrame)
  读循环（不阻塞落盘）
  keepalive report + last_activity
  pending_reports / early_downlink_buf / event_log（设备级 seq）
  Connection + UplinkTurn + DownlinkPlayer
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{turn_id}/
assets/{asset_id}.wav             # 仅 Phase 2 HTTP；只存 WAV
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid
├── state     # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── uplink_frozen   # Phase A 置 true 后禁止再 Enqueue Stage 1/2
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
2. 释放 speak_permit（该 turn_id 的 once，见 §4.15）。
3. 释放槽。
4. **append `turn_terminal` 到 event_log**（每次 Terminal 都必须发，不论有无 TTS）。
5. 复制 waiter 列表。
6. **解锁后再唤醒**（log 必须已对同锁读取可见）。

禁止先唤醒再写 log。禁止 Terminal 而不发 `turn_terminal`。

**上行生产者冻结：** 每帧 Stage=1/2 **入队前**持 `device_mu` 检查：`uplink_frozen` 或 `state==Terminal` 或 Connection ∈ {Disconnecting, Disconnected} → **停止发送且不 Enqueue**。不得只在 Terminal 后才停。

### 4.2 Event

每条事件有 `device_id` 与 **该设备**单调 `event_seq`（从 **1**）。没有进程级全局序号。连接级用 `correlation_id`。Reserved 之后的对话事件用 `turn_id`。

禁止声称所有事件都有 `turn_id`。禁止发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志名。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
| `ack_failure` | `'1'` + `/register/client` 且 `data.code != 0`；随后 `request_finalize(register_nack)` |
| `report_echo` | `/report/client` 的 `ReportData` 命中 `pending_reports[seq]` |
| `report_echo_unmatched` | 回显序号不在表中 |
| `report_timeout` | pending 项超时 |
| `connection_failed` | **仅异常收口**（winning reason 为异常）。Phase C 已 Stopped 且 `Connection=Disconnected` 之后写入；带 `reason` |
| `connection_stopped` | **用户主动收口**：winning reason 为 `user_stop` 或 `user_delete`（本趟实际关了连接）。不设 `last_error` |
| `device_deleted` | **仅用户 delete** 从 Manager 摘键之后。不设 `last_error` |
| `asr_result` | 无前缀 JSON `Action=asr_result`（含 interim；完成判定只认 IsFinal） |
| `command_received` | `'1'` `/command/client` |
| `json_reply` | 无前缀 `Code==0` 且非 asr_result |
| `tts_chunk` | 匹配 UUID 的 `'0'` Stage=1 |
| `tts_done` | ≥1 帧匹配 TTS，且本次经 TTS idle 进入 Terminal |
| `vad` | 收到 Stage=4 |
| `expected_server_drop` | WaitingReply 首包到期；无终态相关下行；无匹配且 **IsFinal=true** 的 asr_result。仅矩阵 drop 行作为通过 |
| `protocol_error` | `Code=1` / `14007` 或非法首字节 |
| `early_downlink_overflow` | 提前下行缓冲溢出 |
| `turn_terminal` | **槽已释放。** 必带 `turn_id`、`turn_end_reason`、`uplink_end_reason`、`reply_kind`。command/JSON/silent/interrupt/timeout/error/connection_lost **一律发送** |

`inferred_no_reply` 是 `expected_server_drop` 的别名，实现只保留一个主类型。

**event_log 与游标（设备级）**

- 每设备一份日志。保留最近 **10000** 条或 **24h**（先到为准）。
- 显式维护 `evicted_through_seq`：已丢弃的最大 `event_seq`。空日志且从未淘汰时为 **0**。淘汰后恒有 `oldest_seq == evicted_through_seq + 1`（若仍有条目）。
- `after_event_seq` 为 **排他**游标：返回/等待 `event_seq > after_event_seq`。
- **410 `event_seq_expired`** 当且仅当 `after_event_seq < evicted_through_seq`。body：`evicted_through_seq`、`oldest_seq`、`newest_seq`。只用 410，不用 409。
- 合法边界：`after_event_seq == 0` 且 `evicted_through_seq == 0`（oldest=1）→ **不**过期。`after_event_seq == oldest_seq - 1` → **不**过期。`after_event_seq == oldest_seq - 2` 且已有淘汰 → **410**。
- 省略 `after_event_seq`：只等 **未来**事件，不回放，不 410。

事件 WS 与 `GET .../events` 均针对 **一台设备**。缺 `device_id` → **400**。

### 4.3 `/wait` 与 speak_and_wait 原子窗

**device_mu** 同时保护：Turn 状态、event_log 追加、waiter 集合。

`POST /wait` 持该锁：

1. 校验 `device_id`；缺则 400。
2. 若有 `turn_id`：无此 Turn → 404；已 Terminal → **立即 200**（读已落库快照）；仍活动 → **同一临界区登记 waiter**，再解锁阻塞。
3. 若按 `event_type`：`after_event_seq < evicted_through_seq` → 410；log 中已有 `seq > after` 的匹配 → 立即 200；否则同一临界区登记 waiter。
4. 禁止「读完状态、解锁、再注册」。

`speak` / `speak_and_wait` 在 **CAS 同一临界区** 记录 `seq_before = newest_seq`（排他：之后回放用 `after_event_seq=seq_before`）并返回 `turn_id`。`speak_and_wait` 同时登记 completion waiter。超时/客户端断开：只摘本 waiter，不 Terminal。

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

**WaitingReply 前：** 不启动完成计时、不 Terminal、不释放槽。失败 JSON 除外（取消表停上行）。发送侧每帧 Stage=1 前检查 §4.1 冻结条件。

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

**等待预算（`--wait` / `speak_and_wait` / Scenario `wait:true` 缺省 timeout）：**

```text
upload + first_reply + max(idle, followup, post_final_asr_silence) + slack
```

禁止默认写死 30s。`post_final_asr_silence` 配得比 idle/followup 更长时，合法 silent **不得**先 504。

### 4.5 Scenario 断言与捕获

TTS 用例：`tts_done` + idle。纯指令：`command_received`，**不要** assert `tts_done`。文本：`json_reply`。槽释放：`turn_terminal` 或 `wait:true`。`asr_contains` 仅 `StreamingAsrTextReply`，optional。

**捕获（CAS 同锁）：** 每步 `speak` 结果含 `turn_id`、`seq_before`（受理前 `newest_seq`）、若 `wait:true` 则还有终态 `event_seq`。后续步骤用 `$prev.turn_id` / `$prev.seq_before`。

**禁止：** `wait:true` 之后再发一条 **无游标** 的 `wait event_type=turn_terminal`（只等未来，必然漏）。`wait:true` 已等到 Terminal 时，删掉重复 wait。若还要 assert 历史事件（如 `tts_done`），必须带 `after_event_seq: "$prev.seq_before"` 和/或 `turn_id`。

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

### 4.7 取消表与出站例外

`uplink_end_reason` 非空不改写。

| 状态 | 优雅关闭时 `finalFrame` | 空的 uplink_end_reason | turn_end_reason |
|------|-------------------------|------------------------|-----------------|
| Reserved | 无（不发 Stage=3） | 保持空 | interrupt / connection_lost |
| Speaking | Stage=3；停 Stage=1 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | Stage=3 | 保持 | 同上 |
| WaitingReply | Stage=3；取消计时器 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

用户 interrupt → `turn_end_reason=interrupt`。连接收口 → `connection_lost`。失败 JSON/本地错误 → `error`。

**出站规则（与 Disconnecting 对齐，禁止自相矛盾）：**

1. Connection ∈ {Disconnecting, Disconnected} 或套接字已死：所有 **公开 Enqueue**（register/report/ACK/Stage 1/2/3）一律拒绝。
2. 优雅关闭需要的 Stage=3 **不得**走公开 Enqueue，只由 `BeginClose(finalFrame)` 注入（见 §4.9）。
3. 异常关闭：`finalFrame=无`，丢弃队列，不发 Stage=3。

### 4.8 report 取号

`report_next_seq` 初值 = `report_sequence_start`（默认 1）。**第一个序号等于 start。**

同一把 `report_mu`：取号、递增、登记 `pending_reports[seq]`。**必须先解锁 `report_mu`，再 writePump Enqueue。** 禁止持 `report_mu` 做 IO。可用 `atomic.AddUint64`（初值 start-1）+ 锁/`sync.Map` 插入 pending。禁止无锁读改写、禁止无锁 Go map。

仅 `kind=initial` 且 Reporting → Ready。initial 超时 → `request_finalize(report_timeout)`。keepalive 与手动 report 共用此路径。

### 4.9 writePump、BeginClose、配置所有权

每个 DeviceInstance **唯一**出站入口。必须经此：register、report（含 keepalive）、音频 Stage=1/2/3、binary/JSON ACK、其它管理帧。

同一 Turn 的 Stage=1 各片与最后 Stage=2 **按入队顺序写出**（由单一上行协程按序 Enqueue），直到被 `uplink_frozen` / `BeginClose` 截断。

**队列参数所有权**

| 项 | 默认 | 所有权 |
|----|------|--------|
| `write_queue_depth` | 256 | **Phase 1**：设备 YAML `behavior`。**Phase 2**：只读 Manager YAML；设备 YAML **禁止**出现这两项，出现则创建/PUT **400** |
| `write_drain_timeout_sec` | 2 | 同上 |

writePump 协程 **禁止**回锁 `manager_mu` / `device_mu` / `conn_mu`。

**`BeginClose(finalFrame)`**（持 writePump 内部锁即可；Phase A 在持 device/conn 锁时调用，writePump **不得**再回锁那两把锁）：

1. 置 `closing=true`。之后公开 Enqueue 失败（不触发第二次 finalizer）。
2. `finalFrame == Stage3`（优雅关闭且取消表需要）：
   - 从队列中 **丢弃尚未写出的本 Turn Stage=1/2**。
   - 将 Stage=3 追加为该连接最后一帧（优先于任何残留 ACK/report：Stage=3 排到队尾且不再接受其它入队）。
3. `finalFrame == 无`（异常关闭、套接字已死、Reserved 无需 Stage=3）：
   - **丢弃**队列剩余帧，不追加 Stage=3。
4. 返回。真正的 drain/close socket 在 Phase B **无实例锁**下进行。

**Phase B：** drain 直到队列空或 `write_drain_timeout_sec`；超时丢剩余；关 websocket；等待读循环退出（同一时限）。**禁止**在 Phase B 再 Enqueue Stage=3。

- 队列满（未 closing）：Enqueue 失败 → `request_finalize(write_backpressure)`；HTTP 尚未成功响应则 **503**。
- 写失败 → `request_finalize(write)`。

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
**必须重连的身份字段：** enterprise、device_type（以及不可变的 device_id）。

**PUT 身份字段（enterprise / device_type）仅当 `instance_state ∈ {Created, Stopped}`。** Starting / Running / Stopping → **409**（避免本 generation 握手身份与持久化配置分叉）。其它非身份字段：Stopped 或 Created 可改；Running 时改非身份字段的规则保持「不改身份即可 200」，但不得改 enterprise/device_type。

### 4.12 锁顺序、register 一次性消费、Running 映射、三段 finalizer（join-wait）

**锁顺序（禁止反转）：** `manager_mu` → `device_mu` → `conn_mu` → `report_mu`。  
**禁止**持上述任一把锁做 drain、close、写盘、等待 `finalize_done`、或调用 `request_finalize`。  
Phase 1 无 Manager：省略 `manager_mu`，其余相同。

**每次成功 occupy 进入 Starting：** `conn_generation++`；`permit_held=true`（仅 Phase 2）。Registering 另分配 `register_attempt_id`。每 generation 有：`finalize_started`、`finalize_committed`、`finalize_done`、`winning_reason`。

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
5. `code!=0`：记下 `ack_failure` 所需字段、`gen`；**解锁**；append `ack_failure`；`request_finalize_async(gen, register_nack)`（读协程不得 wait）。HTTP stop/delete 用同步 `request_finalize` **wait `finalize_done`**。

**谁跑 Phase B/C：** 成为 leader 的调用方 **禁止**在本协程执行 drain/close（读循环若自己等自己退出即死锁；writePump/timer 同理）。leader 只负责 Phase A 后 **启动专用 closer 协程** 跑 B/C 并 `broadcast finalize_done`。读/写/timer 路径用 async：**不 wait**。HTTP stop/delete **始终 wait `finalize_done`**。

**timer callback：** 持 `conn_mu`；generation/attempt 不匹配或 Connection≠Registering → 解锁返回；`try_consume` 失败 → 解锁返回；成功则记下 `gen`，**解锁**，再 `request_finalize_async(gen, register_timeout)`。  
**禁止**依赖 `Timer.Stop` 取消已开始的 callback。

**实例 Running：**

| 路径 | Connection | Starting→Running | speak 前置 |
|------|------------|------------------|------------|
| 正常 | Ready | Ready 达成时 | Running 且 Ready |
| skip_register | 停在 Connected | Connected 达成时 | Running 且 Connected |
| skip_report | 停在 Registered | Registered 达成时 | Running 且 Registered |

GET 同时返回 `instance_state` 与 `connection_state`。禁止注入路径永远停在 Starting。

**`request_finalize(generation, reason)` — 唯一退出入口**

调用方 **不得**持实例锁。stop、delete、握手失败、ACK nack、timeout、读写失败、回压均走这里。同一 generation 只跑 **一次** Phase A/B/C；后来者 **加入并（按调用约定）等待** `finalize_done`。

**并发 reason 优先级（后来者只升不降）：**

```text
user_delete > user_stop > 先到达的异常 reason
```

- 异常之间：Phase A **leader 的第一条异常** 保留为 `winning_reason`（后来的 `write` 等不覆盖，除非升到 user_*）。
- winning 为 `user_stop` / `user_delete` → Phase C 写 `connection_stopped`，清空 `last_error`。
- winning 为异常 → `connection_failed`，`last_error=winning_reason`。
- `user_delete` 在 wait 返回后由 Manager 摘键并写 `device_deleted`。

**算法（持锁段无 IO、无 wait）：**

```text
加锁（规定顺序）
  若 conn_generation != generation → 解锁；return stale（不 wait）
  若 finalize_committed → 解锁；return（stop 幂等 200）
  按优先级 merge winning_reason
  若 finalize_started：
      done = finalize_done
      解锁
      若调用约定为 wait：等待 done；return
      否则 return（async 内部路径）
  // leader
  finalize_started = true；创建 finalize_done
  实例 → Stopping；Connection → Disconnecting
  若 Turn 未 Terminal：uplink_frozen = true；按取消表计算终态字段与 finalFrame
  取消全部 timer（Stop，不等待）
  BeginClose(finalFrame)     // 见 §4.9；异常或 Reserved → finalFrame=无
  快照 websocket
  解锁
  启动 closer 协程（不得是读循环 / writePump / timer 自己）
closer：Phase B drain / close socket / 等读循环退出（无实例锁）
closer：Phase C：加锁
  finalize_committed = true
  Connection → Disconnected
  若 Turn 未 Terminal：取消表（不再出站）走 §4.1
  清空 pending；permit_held 则释放一个 conn_permit
  实例 → Stopped；按 winning_reason 写事件与 last_error
  复制 waiter；broadcast finalize_done
  解锁后唤醒 waiter
wait 型调用方：等待 finalize_done 后再对 HTTP 回 200
```

此时 **不**在 Phase A 释放 `conn_permit`。start 见到 Stopping → **409**。

delete：若仍 Starting/Running/Stopping，`request_finalize(gen, user_delete)` **wait 到 Phase C** 再摘键；已 Stopped 则不调 finalizer、不释放 permit，只摘键并 `device_deleted`。

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

并发测试必须覆盖：重复 start、超限 start、stop∥delete **均等到 Stopped**、stop∥断线 join 同一次收口、ACK∥timeout、finalizer 后来者 wait、drain 期间再 start（409）、关队列后不再入队 Stage 1/2、长 `post_final_asr_silence` 不 504。

批量创建冲突 **整批 409**。`POST /devices/batch/start|stop|delete`。`default_stagger_ms` 默认 50。

### 4.16 REST 媒体（仅 Phase 2）

禁止 HTTP 接受任意服务器本地路径。禁止 HTTP 上传 raw PCM（无 fmt 无法校验时长）。

- `POST /assets`：multipart 字段 **`file` only**，内容必须是 **WAV（RIFF + PCM fmt）**。从 fmt 读取 sample_rate / channels / bits；duration_ms 由 data 块与 fmt 计算。非 WAV、非 PCM fmt、超 `max_asset_bytes` 或超 `max_asset_duration_sec` → 400。写入 `assets_root/{asset_id}.wav`，拒绝 `..`、绝对路径、符号链接逃逸。
- speak 只引用 `asset_id`。受理时把 PCM **拷贝进该 Turn**；之后 `DELETE /assets/{id}` 不影响进行中的上行（204 可立即摘名）。拷贝前文件消失 → 404。
- `stream`：最多 `max_stream_entries`（默认 **16**）条；音频 duration + silence 之和 ≤ `max_stream_duration_sec`（默认 **60**），超限 400。silence 为内部 PCM 全零。
- 下载：`GET .../audio/uplink|downlink` 必须是浏览器可直接播的 **WAV 容器**，`Content-Type: audio/wav`。磁盘上的 `.pcm` 在响应时加 RIFF 头。无文件 404。
- Phase 1 CLI `--audio` 仍读本机 WAV，不经过 HTTP。

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
| **热更新** | playingMode 经 report；身份字段仅 Created/Stopped 可改 |
| Register | 先登记超时再发送；一次性消费；持锁时不调 finalizer |
| Report | 锁内取号；解锁后再 enqueue；首序号=start；匹配回显才 Ready |
| 线上格式 | pcm s16le mono；HTTP 资产仅 WAV |
| ACK | 只看报文标志 |
| 出站 | 全部走 writePump；关闭走 BeginClose |
| 收口 | 三段 + join-wait；Disconnected；permit 在 close 之后 |
| 等待预算 | 含 post_final_asr_silence |
| 配置 | Device 三段、playing_mode ∈ {1,2,3}、UUID 范围 |

## 6. 音频管线

源 WAV 解 RIFF → 内部 PCM → silence=全零采样 → 按 slice_ms 切 **pcm 字节**。禁止逐片封装 WAV。HTTP 下载再包一层 RIFF。Phase 4 才允许线上 mp3/wav 推流（整段只编码一次）。

## 7. 技术选型

硬门槛：按字节收 TextMessage 二进制。Phase 1 **推荐 Go**。真实 core 收到至少一帧 `'0'` TTS 且未因 UTF-8 失败后锁定。

## 8. 阶段（边界不变）

| 阶段 | 交付定义（做完就能用） |
|------|------------------------|
| Phase 1 | 单设备：握手→register→report→pcm 上行→收下一条回复；CLI；落盘；keepalive |
| Phase 2 | 批量+API+Scenario；playingMode 热更新；JSON ACK；WAV 上传与录音下载 |
| Phase 3 | Web UI 只消费 Phase 2 |
| Phase 4 | queue、非 pcm、静默探针 |

## 9. 目录

`protocol/` `core/`（writePump.BeginClose、request_finalize、pending_reports、early_downlink、event_log）`cmd/speak|check|fixture/` `configs/example_device.yaml` `configs/manager.yaml` `configs/templates/` `testdata/` `data/assets/`

## 10. 对齐基线

仓库 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。  
register Redis 出错可不 ACK。管理消息独立 goroutine。MH Seq 例外。不要用 `projects/go/ai-creates-wealth`。
