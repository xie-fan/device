# 玩具设备模拟器 — 整体架构设计文档（v21）

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：

1. 忠实模拟真实玩具设备的 chatbot 协议
2. 批量设备并发（模板、错峰、限额）
3. 参数可配置、可保存
4. Agent REST + 事件 WS
5. 人工调试 UI

协议：`docs/toy-device-websocket-protocol.md` + 本文 §10 对齐基线。主路径 `Action=chatbot`。  
Phase 1 交付物是能跑通的单设备 CLI。

## 2. 设计原则

1. 协议忠实。golden 对照基线 `types.AudioHeader`。`example/asr/mock.go` 禁止作 golden。
2. 失败按 fault 矩阵推断。禁止伪造服务端日志名。
3. Turn 一等公民。`uplink_end_reason` 先写不改。
4. speak：先拷贝 PCM 再 CAS Reserved。
5. API 先于 UI。
6. Phase 1/2 线上 pcm（mono s16le）。HTTP 只接受 WAV。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK 只看报文 `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变；删除后可重用。事件、Turn、录音用必填 `instance_id` 隔离。
10. 推荐 Go（按字节收 TextMessage 中的二进制）。

运输层缓冲称 **outbound buffer（writePump）**。Phase 4 才有 **speak backlog**。

**Turn 打断 ≠ 关连接。** 协议 Stage=3 只停本轮语音，连接继续复用。关连接只用 `BeginClose`。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 模板 / stagger / conn_permit / speak_permit / live + tombstone
DeviceInstance:
  每代 ConnectionSession = 新 WebSocket + 新 writePump
  CancelTurn → CancelResult；BeginClose 过滤保留 Stage=3
  request_finalize 三段 + join-wait
  terminalLocked / appendEventLocked：锁内写日志、摘 HTTP waiter、投入 WS 订阅 inbox
  wsHub：每订阅 backlog 切片 + live inbox + 该 socket 唯一 writer；禁止解锁后 fan-out
  pending_reports / early_downlink_buf / event_log（instance 级）
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{instance_id}/{turn_id}/     # Phase 2
recordings/{device_id}/{turn_id}/                   # Phase 1 单进程
assets/{asset_id}.wav + epoch
```

## 4. 核心抽象

### 4.1 Turn、占用、terminalLocked

```text
Turn
├── turn_id / uplink_uuid
├── state     # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── uplink_frozen
├── injected_fault
├── uplink_end_reason   # 空 | stage2 | interrupt | vad | error | timeout；非空不改
├── turn_end_reason     # idle | interrupt | error | timeout | connection_lost
├── reply_kind          # 空 | tts | command | json | silent | command+tts | json+tts
├── related / early_downlink_buf
├── first_reply_timer / settle_timer
├── pcm（CAS 前已拷贝）
```

每设备一槽。默认 reject → 409。禁止用「第一帧是否已发」判断占用。  
**WaitingReply 之前**禁止因 command / 成功 JSON / 匹配 TTS 将 Turn 置 Terminal。

`uplink_end_reason`：Stage=4 立即记 `vad`（若空）再补 Stage=2；非空则后续 Stage=2/取消不得覆盖。Reserved 取消：保持空。套接字已死且仍空：写 `error`。

**speakable（否则 speak → 409 `not_speakable`）：**

| 路径 | 要求 |
|------|------|
| 正常 | Running 且 Connection=Ready |
| skip_register | Running 且 Connected |
| skip_report | Running 且 Registered |

Starting / Stopping / Created / Stopped 均不可 speak。409 body 含 `instance_state`、`connection_state`。不是 429、不是 404。

**speak 受理顺序（禁止先占槽再读文件；禁止持 `asset_mu` 读完全文件）：**

`asset_id` 与 `stream` 互斥且必须择一，否则 400。

每个 asset：`epoch` 从 1 起。`asset_mu` 只保护元数据（path、epoch、是否存在），**不**保护文件字节。

1. 校验 body。`stream.length > max_stream_entries` 或总时长超 `max_stream_duration_sec` → 400。
2. 短持 `device_mu`：记下 `audio_fp`（sample_rate、channels、sample_format）与 `conn_generation`（无连接=0），立即解锁。
3. 对每个 `asset_id`（含 stream 内 audio 项）：
   - 持 `asset_mu`：不存在 → 404；记下 `path`、`epoch0`；**立即解锁**。
   - **无锁**按 `path` 读全部字节。打开/读失败 → 404。
   - 再持 `asset_mu`：不存在 **或** `epoch != epoch0` → 404（拷贝窗口内 DELETE 已发生）；否则解锁。
   - WAV fmt 必须等于步骤 2 的 `audio_fp`，否则 400。
4. `silence`：内部生成全零 PCM，格式同 `audio_fp`，禁止拼接 RIFF。
5. 持 `device_mu`：`audio_fp` 已变 → 409 `audio_config_changed`；`snap_gen != 0` 且不等于当前 generation → 409 `generation_changed`；再检查 speakable → CAS Reserved → 成功路径内 TryAcquire speak_permit。失败则丢弃 PCM，409/429，**不得**留下 Reserved。
6. 同锁登记 `seq_before`、挂 PCM、speak_and_wait waiter。解锁后由上行协程经 outbound buffer 发送。

`DELETE /assets/{id}`：持 `asset_mu`：`epoch++`，unlink，解锁。不读文件。步骤 3 复验成功之后，内存 PCM 已线性化；之后 DELETE 不影响该次 speak。拷贝/校验失败路径 **均不** 占槽、不 Acquire。

Stage=1/2 入队前检查：`uplink_frozen` 或 Terminal 或 Connection ∈ {Disconnecting, Disconnected} → 停止且不入队。

#### terminalLocked（唯一锁内 Terminal 迁移）

禁止再写「持 `device_mu` 进入 Terminal 并在函数内解锁唤醒」的第二条路径。完成矩阵、`/interrupt`、失败 JSON、Phase C **都必须**调用本函数。

**契约：**

- 调用方 **必须已经持有** `device_mu`。本函数 **不得** 再获取任何锁（含 `device_mu` / `conn_mu` / `writePump_mu`）。
- 本函数 **不得** IO、**不得** Enqueue、**不得** 唤醒 waiter、**不得** 调 `CancelTurn` / `BeginClose` / `request_finalize`、**不得** `WriteMessage`。
- 返回值 `TerminalNotify`（可全空）：`completion`（speak_and_wait / CLI `--wait`）、`eventWaiters`（live `POST /wait`）、`event`（刚写入的 `turn_terminal`）、`slowSubs`（被摘掉的 WS 订阅，仅供日志）。唤醒 HTTP waiter 是调用方在 **释放 `device_mu` 之后** 的职责。解锁后 **禁止** Close / WriteMessage 这些 socket（writer 是唯一 Close 者；writer 未启动的升级失败除外，见 wsHub）。禁止只返回 completion 而把 event waiter 留在表里空等到 504。

**锁内步骤：**

1. 槽空或 `state==Terminal` → 返回空 `TerminalNotify`（幂等，不写第二份 `turn_terminal`，不二次摘 waiter）。
2. `finalize_started==true` 且调用方 **不是** Phase C → 返回空 `TerminalNotify`（完成矩阵 / interrupt / 失败 JSON 在收口开始后不得再置 Terminal）。
3. 写快照：`turn_end_reason` 按入参；`reply_kind` 保持已有（入参可覆盖空值）；`uplink_end_reason` 若仍空则按入参写入（Reserved 保持空，除非套接字已死写 `error`）。
4. `state=Terminal`；取消本 Turn 计时器（只记取消，真正 Stop 可在解锁后）。
5. 释放 `speak_permit`（该 `turn_id` once）。
6. 释放占用槽。
7. 经 **`appendEventLocked`** 写入 `turn_terminal`（必带 `turn_id`、`turn_end_reason`、`uplink_end_reason`、`reply_kind`）。不得绕过它直接 append 日志。
8. 摘走该 Turn 的 completion waiter，与步骤 7 返回的 `eventWaiters` / `event` / `slowSubs` 一并放进 `TerminalNotify`。

#### appendEventLocked（唯一锁内 append + 摘 event waiter + 投入 WS inbox）

任何事件写入（`turn_terminal`、`tts_done`、`command_received`、连接生命周期、`local_validation_error` 等）**必须**走本函数。禁止「只 append 日志、等 /wait 自己再看见」。禁止调用方解锁后按快照通道 `wsFanout`（那会让 `seq=N+1` 先于 `seq=N` 到达）。

返回值 `EventNotify`：`{eventWaiters, event, slowSubs}`。

- 调用方 **必须已经持有** `device_mu`。不得再加锁、不得 IO、不得唤醒、不得 `WriteMessage`。
- 步骤：
  1. 分配 `event_seq` 并 append 日志。
  2. 摘走匹配的 live **event waiter**（HTTP `/wait`）。
  3. 对 hub 中每个订阅（含阶段仍为 `catchup` 者）：若 `event_seq` 匹配过滤，**非阻塞**投入该订阅 **inbox**（不是 backlog 切片）。inbox 上限见 wsHub 两阶段。超上限 → `requestClose(abort)`（已持锁）、记 `slowSubs`。不得阻塞 Turn，不得在本函数 Close socket。
  4. 返回 `EventNotify`。
- HTTP waiter 匹配：同一 `instance_id`；`event_seq > after_event_seq`；未指定 `event_type` 或 type 相等；未指定 `turn_id` 或 turn_id 相等。每个 waiter `try_consume` 一次：被摘走后超时回调 **不得** 再 504。
- WS 订阅匹配：同上。writer 先写完 backlog 切片，再按 catchup 循环把 inbox **swap 到空** 后才置 `live`，最后才按 live 上限收新事件。因拷贝与登记同一临界区，切片末条与其后写出的 inbox 事件之间无空洞、无重叠。禁止把 backlog 推进 inbox。禁止在 inbox 仍有 catchup 积压时置 `live`。
- tombstone 无 live waiter、无 hub。Phase 1 无 REST `/wait` / WS 时，后两字段为空，仍须走本函数写日志。

**同一临界区累积：** 一次持锁内可能连续写入 `tts_done` 再 `turn_terminal`（或 `protocol_error` / `stage3_backpressure` 再 `turn_terminal`）。每次 `appendEventLocked` 的 `EventNotify` **必须累加**到调用方的 `acc`。丢弃中间返回值会使 waiter 已从表中摘走却永不 200。`terminalLocked` 内部那一次也并入 `acc`。`CancelResult=backpressure` 必须在 `terminalLocked` **之前**、仍持 `device_mu` 时再调一次 `appendEventLocked(local_validation_error, reason=stage3_backpressure)` 并入 `acc`。`CloseResult=backpressure` 同此，走 **Phase A 模板**（不得丢进 Phase C 的空 `acc`）。

**外层模板（完成矩阵 / interrupt / 失败 JSON）：**

```text
持 device_mu
  acc = 空 EventNotify 列表
  若本路径还要写 tts_done / protocol_error / command_received 等：
    acc += appendEventLocked(...)
  若需要出站 Stage 3 或丢掉待发 Stage 1/2：
    按取消表算 token
    持 conn_mu → 持 writePump_mu → cr = CancelTurn → 释放泵与 conn
    if cr == backpressure:
      acc += appendEventLocked(local_validation_error, reason=stage3_backpressure)
  n = terminalLocked(...)      # 内部再 appendEventLocked(turn_terminal)
  acc += n 的 EventNotify 部分
释放 device_mu
notify(n.completion)
for e in acc:
  notify(e.eventWaiters)       # 仅 HTTP /wait
  # 禁止 Close / WriteMessage WS。slowSubs 只记账；writer 自行关 socket
# WS 正文只由各订阅 writer 先写 backlog 切片、再从 inbox 弹出后写出
```

**Phase C 模板（已持 `device_mu`，无 IO）：**

```text
finalize_committed = true（本 generation）
acc = 空
n = terminalLocked(turn_end_reason=connection_lost, 调用方=Phase C)
acc += n 的 EventNotify 部分
speakableWaiters = 摘走键匹配的 speakable_waiters
... Stopped / 连接事件：acc += appendEventLocked(...) / permit ...
broadcast finalize_done
释放 device_mu
409 唤醒 speakableWaiters
notify(n.completion)
for e in acc:
  notify(e.eventWaiters)
  # 禁止 Close / WriteMessage WS
```

Phase C **禁止** 在 `terminalLocked` / `appendEventLocked` 之外再「取出 Turn completion 或 event waiter」。禁止调用任何会自行加 `device_mu` 的 Terminal 包装函数。禁止解锁后按通道列表 fan-out 事件。

**Phase A 模板（finalizer leader，已持 `device_mu`）：**

```text
acc = 空
finalize_started = true
Stopping；Disconnecting；uplink_frozen；完成矩阵关闭
token = 按取消表（已 Terminal → 无；须保留 buffer 里已有 Stage=3）
持 conn_mu → writePump_mu → cr = BeginClose → 释放泵与 conn
if cr == backpressure:
  acc += appendEventLocked(local_validation_error, reason=stage3_backpressure)
不释 permit
释放 device_mu
for e in acc:
  notify(e.eventWaiters)    # 必须在进入 Phase B 之前；禁止丢给 Phase C 的空 acc
启动 closer：Phase B（无 device_mu）→ 再持锁跑 Phase C 模板
```

BeginClose **不得** 自己 `appendEventLocked`。禁止把 Phase A 的 `EventNotify` 丢弃后指望 Phase C 再摘 waiter。

#### wsHub（live 订阅：backlog 切片 + catchup 排空后才 live + close_mode + 每连接单 writer）

每个 live instance 一个 hub。tombstone 不登记。256 **不是** backlog 上限，也不是 `write_queue_depth`。

订阅阶段（只在 `device_mu` 内改）：登记时为 `catchup`。**禁止**在 inbox 非空时置 `live`。唯一切换：writer 持 `device_mu` 看见 inbox **为空** 且 `close_mode==open`，同一临界区置 `live`。

`T = write_drain_timeout_sec`，必须 **`> 0`**（不新增键）。Phase 1：`<=0` 启动拒绝（只约束设备泵）。Phase 2 Manager：`<=0` 加载失败；该键同时约束设备泵 Phase B 与事件 WS。

**`close_mode`（只在 `device_mu` 内改；禁止只用 close inbox 表达两种退出）：**

| 值 | 含义 |
|----|------|
| `open` | 正常。登记时为此值。 |
| `drain` | 已摘 hub。writer **必须写完** 剩余切片与 inbox（含 `device_deleted`）再 Close，**不得**再进 live。 |
| `abort` | 已摘 hub。writer **丢弃**剩余，Close。覆盖 `drain`。 |

`requestClose(mode)`：锁内步骤为 `abort` 覆盖一切；仅 `open` 可进 `drain`；摘 hub；**inbox 只 close 一次**（已关闭则跳过，禁止二次 close）。close inbox 只负责唤醒，不编码模式。调用方已持 `device_mu` 则只跑锁内步骤，**禁止重入加锁**。

| 原因 | mode |
|------|------|
| 删除：已 `appendEventLocked(device_deleted)` | `drain` |
| 客户端 close 帧，或 reader 的 `ReadMessage` 错误 | `abort` |
| inbox 过载 | `abort` |
| WriteMessage / Ping 超时或失败 | `abort` |
| writer 在 `idle_deadline` 看见本次 Ping 无 Pong | `abort` |

事件 WS **禁止** `SetReadDeadline`。Gorilla（基线 `v1.5.3`）把 `SetReadDeadline` / `ReadMessage` / `SetPongHandler` 都算读侧，不得与 reader 并发调用；且 **read timeout 之后连接读状态损坏，后续 Read 必失败**。因此不得用 socket 读超时做半开探测，也不得「deadline 到期后继续 Read」。

| 对象 | 规则 |
|------|------|
| backlog 切片 | 登记时拷贝 `seq > after` 且匹配过滤的日志（升序）。容量只受日志条数约束，**不是** 256。 |
| inbox | 只由 `appendEventLocked` 非阻塞 push。登记时为空。`catchup` 上限 = `event_log_max_entries`；`live` 上限 = **256**。 |
| last_pong | 只在 `device_mu` 内更新。reader 在启动 `ReadMessage` 循环 **之前**（同一协程）`SetPongHandler` 一次；Pong 回调里写入 now。writer 不得调任何读侧方法。 |
| writer / reader | writer 唯一 `WriteMessage` / `SetWriteDeadline` / `Ping`（`WriteControl`）/ `Close`（升级失败且 writer 未启动除外）。reader 只 `ReadMessage`。`ReadMessage` 错误或 close 帧 → `requestClose(abort)`。writer `Close` 可与 reader 并发，用于唤醒。 |
| 登记 | 持锁：410 → 拷贝切片 → 空 inbox、`catchup`、`close_mode=open` → 入 hub。禁止解锁后发 backlog 再登记。 |
| 升级失败 | 已登记则 `requestClose(abort)`，**HTTP 处理协程 Close**（writer 未启动，唯一非 writer Close）。 |
| 写出 deadline | 每次 `WriteMessage`：`SetWriteDeadline(now+T)`。禁止 `SetReadDeadline`。 |
| 空闲半开（总预算一个 T） | **独立 timer，不碰读 deadline。** 进入空闲时固定 `idle_deadline=now+T`。`now+T/2` 仍无事件且仍 `open` → Ping，`SetWriteDeadline(idle_deadline)`（剩余时间，禁止再加一个 T）。Pong 只更新 `last_pong`。到 `idle_deadline`：**writer** 持锁看 `last_pong` 是否覆盖本次 Ping；是则开新空闲窗口（新 timer）；否则 `requestClose(abort)` 并 `Close`（唤醒 reader）。从进入该次空闲到半开 abort **≤ T**。Pong 成功后必须能跨多个 T 窗口存活。 |
| tombstone | 不入 hub。单协程写 backlog（含 `device_deleted`），每条 `now+T`；超时 `abort` 放弃剩余。客户端断开同 abort。不空等。 |

**writer 顺序（禁止「先置 live 再排空」；每次写出前检查 `close_mode`）：**

```text
abort():
  requestClose(abort)
  丢弃未写出的切片 / drain / inbox 剩余
  Close socket；退出

writeOne(ev):
  持 device_mu: 若 close_mode==abort → 释放后 abort()
  释放
  SetWriteDeadline(now+T)
  WriteMessage；失败或超时 → abort()

对 backlog 切片每条: writeOne(ev)    # drain 不跳过切片（保序）；abort 则停

loop:  # catchup
  持 device_mu
    若 close_mode==abort: 释放; abort()
    若 inbox 空:
      若 close_mode==drain: 释放; Close; 退出   # 已排空，不进 live
      phase=live; 释放; break
    drain = 取出 inbox 全部（仍 catchup）
  释放
  对 drain 每条: writeOne(ev)

live:
  loop:
    持 device_mu
      若 close_mode==abort: 释放; abort()
      若 close_mode==drain:
        若 inbox 空: 释放; Close; 退出
        drain = 取出全部; 释放
        对 drain 每条: writeOne(ev)
        continue
      若 inbox 非空:
        drain = 取出全部; 释放
        对 drain 每条: writeOne(ev)
        continue
    释放
    # 空闲：idle_start=now；idle_deadline=idle_start+T（独立 timer；禁止 SetReadDeadline）
    等到 inbox 可读/关闭、或 now>=idle_start+T/2、或 now>=idle_deadline
      到 T/2 且尚未 Ping 且仍 open: 记下 ping_at；Ping（WriteDeadline=idle_deadline）；失败 → abort()
        # 同一窗口继续等到 idle_deadline，不新开一个 T
      到 idle_deadline: 持锁看 last_pong 是否 >= ping_at；
        是则新窗口（新 timer）
        否则 requestClose(abort); abort()   # Close 唤醒 reader
      inbox 变化: continue 回 loop 顶
```

正常路径（写出成功且 `open`）必须写完 catchup 再置 `live`。`drain` 写完剩余含 `device_deleted`。`abort` 放弃剩余。这不是第二套 inbox 准入。

`GET /ws/events` live：持锁内 410+登记 → 解锁 → 升级 → 启动 writer 与 reader。

`finalize_started==true` 之后，非 Phase C 不得把 Turn 置 Terminal。speak → 409。writePump **禁止**为看 Turn 而回锁 `device_mu`/`conn_mu`。

### 4.2 Event、游标、tombstone（唯一路由）

`POST /devices` 分配 `instance_id`。`event_seq` 从 1，属于该 instance。每条事件含 `device_id`、`instance_id`、`event_seq`。连接级用 `correlation_id`。Reserved 之后的对话事件带 `turn_id`。

禁止发出 `device_not_found`、`status_invalid`、`no_active_turn` 等 **服务端日志名**（HTTP 可用自己的 error 字段，例如 `interrupted:false`）。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出。Stage=3 因 `len >= depth` 未能入队时 reason=`stage3_backpressure` |
| `ack_failure` | `/register/client` 且 `data.code != 0` |
| `report_echo` | ReportData 命中 `pending_reports[seq]` |
| `report_echo_unmatched` | 序号不在 pending |
| `report_timeout` | pending 超时 |
| `connection_failed` | 仅异常收口；Phase C 之后；带 reason |
| `connection_stopped` | 用户主动收口且本趟关了连接；不设 last_error |
| `device_deleted` | 摘 live **之前**写入该 instance 日志的最后一条 |
| `asr_result` | Action=asr_result；完成只认 IsFinal=true |
| `command_received` | `'1'` + `/command/client` |
| `json_reply` | 无前缀 Code==0 且非 asr_result |
| `tts_chunk` | 匹配 UUID 的 `'0'` Stage=1 |
| `tts_done` | ≥1 帧匹配 TTS 且经 TTS idle 进入 Terminal |
| `vad` | Stage=4 |
| `expected_server_drop` | 见 §4.4 终止表；仅 fault 矩阵 drop 行作为通过 |
| `protocol_error` | Code=1 / 14007 或非法首字节 |
| `early_downlink_overflow` | 提前下行缓冲溢出 |
| `turn_terminal` | 槽已释放。必带 turn_id、turn_end_reason、uplink_end_reason、reply_kind |

`inferred_no_reply` 是 `expected_server_drop` 的别名，只保留一个主类型。

**游标（GET events、`POST /wait`、WS **同一套**，禁止第三种省略语义）：**

- 每 instance 一份 log。10000 条或 24h（先到为准）。tombstone 中的 log **冻结**：不会再 append。
- `evicted_through_seq`：已丢弃的最大 seq；从未淘汰为 0。有条目时 `oldest_seq == evicted_through_seq + 1`。
- `after_event_seq` 排他：返回/等待 `event_seq > after`。
- 410 `event_seq_expired` 当且仅当 `after < evicted_through_seq`。body：`evicted_through_seq`、`oldest_seq`、`newest_seq`。
- `after=0` 且 `evicted_through=0` → 不过期。`after == oldest-1` → 不过期。`after == oldest-2` 且已淘汰 → 410。
- **省略 `after_event_seq` ≡ `after_event_seq=0`（从当前 oldest 起）。** 禁止再解释成「只等未来」。只要未来、不要 backlog：调用方必须显式传入当前 `newest_seq`。

| 接口 | live | tombstone（冻结） |
|------|------|-------------------|
| `GET .../events` | 200：`seq > after` 的已有条目（省略则从 oldest 到 newest） | 200：同上，含 `device_deleted` |
| `POST /wait` | 历史已命中 → 200；否则登记 event waiter。未来命中由 `appendEventLocked` 摘走，解锁后 200。禁止日志已有事件仍 504 | **只查已有条目**。命中 → 200。未命中 → **404**（禁止 504，禁止登记 future waiter） |
| `GET /ws/events` | 见 §4.1：`close_mode` 区分 drain/abort；inbox 空才 live；单次写出 `now+T`；空闲用独立 timer，禁止 `SetReadDeadline` | 不入 hub。单协程写 backlog（含 `device_deleted`），每条 `now+T`；超时 abort。不空等 |

**日志解析（events / `/wait` / WS / turns / 录音。`wait_ready` 见 §4.3）：**

`/wait`、WS、`GET .../events`、`GET .../turns*`、`GET .../audio/*` **必填** `device_id` 与 `instance_id`。缺 → 400。

```text
按 instance_id 精确查找（不是「按 device_id 猜当前 live」）：
  若 live 表中该 device_id 的 instance_id 等于请求值
      → 使用 live
  否则若 tombstone[instance_id] 存在
       且 tombstone.device_id 等于请求的 device_id
       且未超过 event_log_ttl_hours
      → 使用 tombstone
  否则 → 404
```

因此：**TTL 内**用旧 `instance_id` 访问 events / `/wait` / WS / turns / 录音，能读到该世系，**不会**接到重建后的新 live。TTL 外或 ID 从未存在 → 404。  
**`GET /devices/{id}` 不走 tombstone：** 摘 live 后一律 404。  
重建同 `device_id` 得到新 `instance_id`，seq 从 1。旧 `turn_id` 配新 `instance_id` → 404。

删除步骤：如需则 wait finalizer → 持 `device_mu`：`acc += appendEventLocked(device_deleted)`（只 push **inbox**）→ `requestClose(drain)`（**不** Close socket）→ 整份 log 移入 tombstone → 摘 live → 解锁后 `notify(acc.eventWaiters)`。writer 按 `drain` 写完剩余（含 `device_deleted`）；若期间 reader 失败则 `abort` 覆盖，放弃剩余。禁止在 push 之前 `requestClose`。`device_deleted` 的 HTTP waiter 不得丢弃 `EventNotify`。磁盘录音保留至 TTL（与 event_log 相同），过期可删。

### 4.3 `/wait` 与 wait_ready

`device_mu` 保护 Turn、log、**三张 waiter 表**（completion / event / speakable）、**wsHub 订阅集**。检查与登记同一临界区。禁止读完再解锁再注册。event waiter 只由 `appendEventLocked` 摘走。live WS 只经 hub 的 backlog 切片与 inbox，不经调用方 fan-out。

必填 `device_id`、`instance_id`。另需 `turn_id` 或 `event_type`。游标见 §4.2。

**实例解析（wait_ready / `event_type=speakable` 与事件回放共用 §4.2 路由，但 HTTP 语义不同）：**

| 命中 | `GET /devices/{id}` | `GET .../events` / `/wait` 历史 / turns / 录音 | `wait_ready` / `event_type=speakable` |
|------|---------------------|-----------------------------------------------|----------------------------------------|
| live 且 instance_id 匹配 | 200 | 200 | 按下表判定 |
| TTL 内 tombstone 精确命中 | **404**（已摘 live） | **200**（冻结日志 / 若文件仍在） | **409** `generation_gone` |
| 未命中或 TTL 过期 | 404 | 404 | 404 |

禁止拿新 live 的 `conn_generation` 去裁决旧 `instance_id`。

**`wait_ready` / `event_type=speakable`：** 必填 `conn_generation`。waiter 键 = `(instance_id, conn_generation)`。  
200 body：`{ "device_id","instance_id","conn_generation","connection_state" }`。

**仅当命中 live 且 instance_id 匹配时**，同锁按下列顺序判定（不得颠倒；committed 优先于 speakable）：

1. 请求的 `conn_generation` 对本 instance 已 `finalize_committed==true` → **立即 409** `generation_gone`（**无论该代曾否 speakable**）。
2. 当前 live 的 `conn_generation` 与请求不同 → 409 `generation_gone`（上一代 waiter 不得被新一代 Ready 唤醒）。
3. 已 speakable **且** generation 匹配 **且** 未 committed → 立即 200。
4. 否则登记 `speakable_waiters[键]`。

**唤醒与超时互斥（与 register 的 `try_consume` 同类）：** 每个 waiter 只能被消费一次。

- 该 generation 变为 speakable：摘匹配 waiter，解锁后 **200**。
- Phase C 持锁：对本 generation 置 `finalize_committed=true`，**取出键匹配的全部** `speakable_waiters`，并按 §4.1 调用 `terminalLocked`；**解锁后以 409 `generation_gone` 唤醒** speakable waiter。禁止只在 speakable/Ready 时唤醒。
- 超时回调：若 waiter 仍在表中 **且** 该 generation **尚未** `finalize_committed` → 摘掉并 **504**。若已被 Phase C 摘走或已 committed → **不得** 再 504（结果已是 409）。

其它 `event_type`（含 `turn_terminal`）：游标过期 410；历史命中 200；live 未命中则登记 **event waiter**（与 completion waiter 分表）；tombstone 未命中 → 404。`turn_id`：无 404；已 Terminal 且历史命中 200。

event waiter 只能由 **`appendEventLocked` 摘走** 后在解锁路径 200。禁止指望 `/wait` 再扫一遍日志。日志里已有事件但 waiter 仍在表中直到 504，视为实现错误。超时回调：waiter 仍在表中才 504；已被摘走则不得 504。

`speak_and_wait`：CAS 同锁登记 completion waiter。超时只摘 waiter，不 Terminal。completion 由 `terminalLocked` 摘出；同一 `turn_terminal` 的 event waiter 由其中的 `appendEventLocked` 摘出；外层解锁后唤醒 HTTP waiter。WS 已在 `appendEventLocked` 投入 inbox。

### 4.4 相关下行、提前缓存、完成矩阵（全文）

基线可不下发 TTS（纯 command、成功 JSON、空 replyText）。

**关联**

| 种类 | 形态 | 本轮判定 |
|------|------|----------|
| TTS | `'0'` | UUID == uplink_uuid |
| command | `'1'` `/command/client` | 无 UUID；Turn 已 Speaking（发过 `'0'`）或之后 |
| asr_result | `{` Action=asr_result | SessionID == uuid 十进制；记录 IsFinal |
| 成功 JSON | `{` Code==0，非 asr_result | 同 command |
| 失败 JSON | Code=1 / 14007 | 占用；立即停上行 |

首次收到：经 `appendEventLocked` 写事件、按需 ACK、录帧、落盘；未 WaitingReply 则入 `early_downlink_buf`（容量 32，溢出丢最旧并 `early_downlink_overflow`）。

WaitingReply 前：不启动完成计时、不 Terminal、不释槽（失败 JSON 除外）。回放只驱动计时器，禁止二次 ACK/事件/录帧/落盘。

**计时（仅 WaitingReply 起）**

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态下行取消；默认 20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| 非音频 followup | command/成功 JSON 且尚无 TTS | 默认 5s；其后有 TTS 则改 idle |
| post_final_asr_silence | first_reply 到期且已有 IsFinal=true 且无终态下行 | 默认 5s → silent。interim-only 不得启动 |

**终止（均经 `terminalLocked` 发 `turn_terminal`，响应须带回这些字段）：**

| 路径 | 计时结束 | reply_kind | turn_end_reason | 额外事件 | 泵操作 |
|------|----------|------------|-----------------|----------|--------|
| 仅 TTS | TTS idle | tts | idle | tts_done | 无 |
| 仅 command | followup idle | command | idle | 无 tts_done | 无 |
| 仅成功 JSON | followup idle | json | idle | 无 tts_done | 无 |
| command 后 TTS | 改 TTS idle | command+tts | idle | tts_done | 无 |
| JSON 后 TTS | 改 TTS idle | json+tts | idle | tts_done | 无 |
| 仅 IsFinal、无终态下行 | silent idle | silent | idle | 无 tts_done | 无 |
| 仅 interim 或全无（正常路径） | first_reply 到期 | 空 | timeout | 无 expected_server_drop | 无 |
| 仅 interim 或全无（fault 矩阵 drop 行） | first_reply 到期 | 空 | timeout | expected_server_drop | 无 |
| 失败 JSON | 立即 | 空 | error | protocol_error | **CancelTurn**（连接保持） |
| 用户 interrupt | 取消表 | 保持已有或空 | interrupt | 无 tts_done（除非此前已发） | **CancelTurn**（连接保持） |
| 连接收口 Phase C | 取消表 | 保持已有或空 | connection_lost | 无 | **BeginClose**（关连接） |

完全无包的静默成功与 drop 在正常路径不可区分：`timeout`。探针属 Phase 4。

`finalize_started` 后本表不进入 Terminal（仅 Phase C）。

等待预算：`upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。禁止默认写死 30s。

### 4.5 Scenario

TTS 用例 assert `tts_done`。纯指令 assert `command_received`，不要 `tts_done`。文本 `json_reply`。捕获 `turn_id`、`seq_before`、`instance_id`。禁止 `wait:true` 后再无游标等 `turn_terminal`（应显式 `after_event_seq=$prev.seq_before`）。

Scenario 的 `batch_start` 默认 `wait_ready:true`，使用本次 start 返回的 `instance_id` 与 `conn_generation`。HTTP `POST /devices/batch/start` 仍 202、不等待。

运行端点：`POST /scenarios/run`，查询：`GET /scenarios/runs/{run_id}`。body 与字段见同目录 `phase2.md` §6.9（该节写全 steps 样例）。

### 4.6 注入矩阵

不查 Mongo。skip_register：夹具 `sim_sr_{run_uuid}_{n}`，本趟不预注册，写入 `testdata/fixtures/fresh_ids.jsonl`，**新建**实例。

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1；服务端无 ASR session | drop |
| oversize | payload>51200 | drop |
| bad_header | `'0'` + 其后少于 100 字节 | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

### 4.7 取消表、CancelTurn、BeginClose

协议：Stage=3 打断本轮；连接继续。Stage=3 不得用关连接来实现。

| 状态 | CancelTurn（连接继续） | BeginClose 优雅关连接 | 空的 uplink_end_reason | turn_end_reason |
|------|------------------------|----------------------|------------------------|-----------------|
| Reserved | 不发 Stage=3；清本 Turn 待发 1/2 | 过滤丢数据帧；无 Stage=3 则 keep 空 | 保持空 | interrupt / connection_lost |
| Speaking | 停 Stage=1；发 Stage=3 | 停 Stage=1；发 Stage=3 后关连接 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 **未发出** | **不发** Stage=2；发 Stage=3 | 同左后关连接 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发出 | 发 Stage=3 | 同左后关连接 | 保持 | 同上 |
| WaitingReply | 发 Stage=3；取消计时器 | 同左后关连接 | 保持 | 同上 |
| Terminal | 无出站（幂等） | **不追加** Stage=3；**保留**已入队未写出的 Stage=3 | 不变 | 不变 |

套接字已死或异常关闭：不发 Stage=3。异常关连接：BeginClose token=无。

调用方已持 `device_mu`→`conn_mu`；再持 `writePump_mu`。泵协程 **永不** 回锁 device/conn。锁顺序允许 `device → conn → writePump_mu`。

**`CancelTurn(turn_id, stage3_token)`（不断连）→ `CancelResult`：**

`CancelResult` 三值（实现必须返回，禁止忽略）：

| 值 | 何时 |
|----|------|
| `none` | `token=无`，或按取消表本就不会入队 Stage=3（Reserved） |
| `enqueued` | Stage=3 已在 buffer（含同 uuid 未写出 Stage=3 的幂等成功） |
| `backpressure` | `token=Stage3` 且删完本 Turn Stage 1/2 之后 `len >= depth`，未能入队 |

步骤：

1. **禁止** 置 `closing=true`。公开 Enqueue 对 **其它** 载荷（ACK、report、后续 Turn）仍然成功。
2. **先**从 outbound buffer 删除该 `turn_id` 尚未写出的 Stage=1 与 Stage=2。不得删除 ACK、report、其它 turn_id、已入队的 Stage=3。
3. `token=无` → 返回 `none`。
4. `token=Stage3`：先看同 uuid 是否已有未写出 Stage=3（有则 `enqueued`，不追加）；否则 `len >= depth` → `backpressure`；否则入队 → `enqueued`。不阻塞、不重试。
5. 若某一帧 **已经从 buffer 取出并正在 `Write`**：视为已发出，不得半截撤回。
6. 返回。泵继续运行。

调用方仍持 `device_mu`：`backpressure` 时必须 `appendEventLocked(local_validation_error, reason=stage3_backpressure)` 并入 `acc`。HTTP `/interrupt` 仍 200（本地已打断）。服务端可能收不到 Stage=3。Turn 仍走 `terminalLocked`。

**`BeginClose(stage3_token)`（关连接，仅 finalizer Phase A）→ `CloseResult`：**

`CloseResult` 与 `CancelResult` 同三值：`none` / `enqueued` / `backpressure`。实现必须返回，禁止忽略。

Turn 已 Terminal 时 **不得再生成** Stage=3（无法从取消表重生）。因此 token=无 **禁止**「清空全部 buffer」——那会吞掉 CancelTurn 已追加、尚未写出的 Stage=3。

**算法（过滤，不是先清空再想起来）：**

```text
closing = true
keep = []
for frame in outbound buffer:
  if frame 是尚未写出的 Stage=3:
    同一 uplink_uuid 已在 keep 中则跳过，否则 keep.append(frame)
  else:
    丢弃（Stage=1/2、ACK、report、keepalive）
正在 Write 的帧视为已发出，不进入 keep、也不撤回
# 此后 §4.9 的 len 只等于 len(keep)。禁止用过滤前的 buffer 长度判断 Stage=3。
# 禁止先往旧 buffer Enqueue 再 outbound=keep（新帧会被覆盖丢掉）。
if token == Stage3:
  若 keep 已有该 uuid 的 Stage=3：CloseResult=enqueued（不追加）
  否则若 len(keep) >= depth：CloseResult=backpressure（不追加）
  否则 keep.append；CloseResult=enqueued
if token == 无:
  不追加新 Stage=3
  CloseResult=none
  # keep 里若有 CancelTurn 留下的 Stage=3，原样保留
outbound buffer = keep
返回 CloseResult
# 本函数不得 appendEventLocked。Phase A 按 §4.1 Phase A 模板把 CloseResult 写入 acc 并在进 Phase B 前 notify HTTP waiter。
Phase B: drain keep（必须写出保留的 Stage=3）直到空或 write_drain_timeout_sec，然后关 socket
```

异常关连接 / 套接字已死：token=无且 keep 为空（无法写出）。Reserved 且从未 CancelTurn：keep 为空，不发 Stage=3。

禁止用 `BeginClose` 实现 `/interrupt` 或失败 JSON。禁止用 `CancelTurn` 实现 stop/delete。

### 4.8 report

`report_next_seq` 初值 = `report_sequence_start`。第一个序号等于 start。  
`report_mu` 内：取号、递增、登记 pending。**解锁后再 Enqueue。** 仅 `kind=initial` 且 Reporting → Ready。新 generation 清空 pending，序号复位为 start。keepalive 与手动 report 共用。

### 4.9 outbound buffer

每代新 writePump。默认深度 **256**。**`write_queue_depth` 必须 `>= 2`**：Phase 1 配置小于 2 → 启动拒绝；Phase 2 Manager YAML 小于 2 → 加载失败 / `cmd/check` 非 0。不新增配置键。Phase 1 在设备 YAML；Phase 2 只在 Manager YAML，设备配置出现则 400。

`write_drain_timeout_sec` 必须 **`> 0`**：Phase 1 `<=0` → 启动拒绝；Phase 2 Manager `<=0` → 加载失败 / `cmd/check` 非 0。Phase 1 只约束设备泵 Phase B。Phase 2 同时约束设备泵 Phase B **与** 事件 WS（单次写出 `now+T`；空闲探测总预算一个 T，见 §4.1）。不是新键，也不是 speak backlog。

**单一物理队列：** 元素为待写出帧；`len` = 已入队尚未取出 Write 的帧数（正在 Write 的那一帧不计 `len`）。容量 `depth = write_queue_depth`。禁止再维护「逻辑控制槽占用」第二条队列。

**唯一准入（一律用 `>=`，禁止 `==` 边界，禁止「其它 uuid 占用控制槽」谓词）：**

| 种类 | 入队 | 拒绝 |
|------|------|------|
| 数据帧（Stage=1/2、ACK、report、keepalive） | `len < depth-1` | `len >= depth-1` → 数据满：`request_finalize_async(write_backpressure)`；HTTP 未响应则 503 |
| Stage=3 | `len < depth` | `len >= depth` → `CancelResult/CloseResult=backpressure` |
| Stage=3 同 `uplink_uuid` 已有未写出帧 | 不追加第二帧，返回 `enqueued`（幂等；先于 `len` 判断） | — |

因此：数据最多占 `depth-1` 格，队列数据满时仍能再入 **至少一帧** Stage=3。不同 uuid 的 Stage=3 只要 `len < depth` 均可入队。不存在第三套「控制槽已被占用」拒绝。

Stage=3 **不走** 数据满的 503：`CancelTurn` / `BeginClose` 仍完成本地 Terminal / closing。`backpressure` 只产生 `local_validation_error`/`stage3_backpressure`（经 `appendEventLocked` 入 `acc`）。`BeginClose` 的准入 `len` **只取过滤后的 keep**。`CloseResult=backpressure` 且 keep 中已无 Stage=3 时，Phase B 直接关 socket。

写失败：`write`。旧泵废弃，禁止把 `closing` 拨回 false。`CancelTurn` 之后同一泵继续服务。

### 4.10 ACK

运行时只看报文标志。`sleep_ms: 0` 不写节流缓存、不能清旧值。

Phase 1：音频+指令 binary，SleepMs=0。  
Phase 2：允许 json、非零 SleepMs、A/B/C。

音频 binary：Ack=音频 Seq；DownlinkType TTS=1 / 提示音=2；Code/SleepMs=配置。  
音频 JSON：ack 与 sequence_number=音频 Seq；downlink_type=`tts`/`hint_audio`；uuid=音频 UUID。  
指令 binary：Ack=指令 sequence_number（缺省 0）；DownlinkType=**3**。  
指令 JSON：ack 与 sequence_number=指令序号；downlink_type=`command`；topic=原指令完整 topic；uuid 省略或 0。

binary：`'4'`+28 字节。json：`'1'` + `.../downlink-ack/server`。

A/B/C：同一 **device_type** 且 DownlinkAck=true；三台不同 ID；status=1；TTS≥2 片。A=binary/0，B=binary/500，C=json/500。

### 4.11 PUT allowlist

`device_id` 创建后不可变（出现在 PUT body → 400）。Phase 2 设备配置禁止 `write_queue_depth` / `write_drain_timeout_sec`（400；这两项只在 Manager YAML）。

| 字段 | Created/Stopped PUT | Starting/Running/Stopping PUT | Ready `POST /report` |
|------|---------------------|-------------------------------|----------------------|
| enterprise, device_type | 200 | **409** | — |
| playing_mode | 200（只写入配置，供下次 start） | **409** | **唯一热更路径** |
| audio.*（format/rate/channels/sample_format/slice_ms/max_payload） | 200 | **409** | — |
| server.url, uuid.*, action, firmware, nic_* | 200 | **409** | — |
| downlink_ack.* | 200 | **409** | — |
| behavior 超时 / keepalive / report_sequence_start / auto_register\|report | 200 | **409** | — |
| recording.* | 200 | 200 | — |
| write_queue_depth / write_drain_timeout_sec | **400** | **400** | — |
| device_id | **400** | **400** | — |

PUT `playing_mode` 在 Running/Ready **不得** 200。热更必须 report，且仅 Ready。

### 4.12 锁、代际、finalizer、Created

锁顺序：`manager_mu` → `device_mu` → `conn_mu` → `report_mu`。Phase A 与 CancelTurn 可在持 conn 时取 `writePump_mu`。泵协程不得取 manager/device/conn。持实例锁禁止 drain、关 socket、wait `finalize_done`、调用 `request_finalize`、唤醒 waiter。

**start：** 仅 Created 或 Stopped，否则 409 且不 TryAcquire。Acquire 失败 429。成功：`conn_generation++`、`permit_held=true`、Starting、**新 writePump**、`finalize_started/committed=false`、新 `finalize_done`、清空 pending 与 early_downlink、`uplink_frozen=false`。槽非空不得 start。

**register：** 持 conn_mu 置 Registering、`register_attempt_id`、settle=pending、启动 timer，**解锁后** Enqueue。ACK 与 timeout 做 `try_consume`；失败解锁后 `request_finalize_async`。`Timer.Stop` 不等待已开火 callback。skip_register 不发送、不装 timer，停 Connected。

**Running 映射：** 正常 Ready；skip_register Connected；skip_report Registered。

**request_finalize：** 后来者 join `finalize_done`。HTTP stop/delete **wait**。读/写/timer **async**。leader 只启动 **closer 协程** 跑 B/C。reason：`user_delete` > `user_stop` > 先到异常。

Phase A：按 §4.1 **Phase A 模板**（`cr=BeginClose`；`backpressure` 写入 `acc` 并在进 Phase B **之前** notify HTTP waiter；不释 permit）。  
Phase B：drain / 关设备 socket / 等读循环（时限 `write_drain_timeout_sec`）。事件 WS 不走 Phase B；按 `close_mode` 由各自 writer Close。  
Phase C（已持 `device_mu`，无 IO）：按 §4.1 Phase C 模板执行。不得调用会自行加锁的 Terminal 包装。不得留给 wait_ready 504。不得承接 Phase A 已摘走的 event waiter。

**Created / Stopped 无连接时（无新 generation、无 permit）：**

| 操作 | 行为 | 事件 | HTTP |
|------|------|------|------|
| stop @ Created | → Stopped，不调 finalizer | 无 `connection_stopped` | 200 |
| stop @ Stopped | 幂等 | 无新事件 | 200 |
| delete @ Created 或 Stopped | tombstone + 摘 live | 仅 `device_deleted` | 200 |
| stop/delete @ Starting/Running/Stopping | join-wait finalizer；delete 再 tombstone | 见上 | 200 |

Created 的 stop/delete **不是 409**。批量同此。`GET /devices/{id}` 在摘 live 后一律 404（即使 tombstone 仍在 TTL 内）。

### 4.13 keepalive

Ready 后每 `keepalive_interval_sec`（默认 60）发 report。刷新 `last_activity`（入站或成功出站）。防服务端约 360s 空闲踢线。skip_report 不停 Ready，不发 keepalive。

### 4.14 读循环

独立读协程持续读。指令 / ASR / TTS 头立即经 `appendEventLocked` 进事件（解锁后再 notify HTTP waiter；WS 已在锁内入 inbox）。录帧与落盘异步。禁止在读协程同步写盘。`finalize_started` 后仍可读，但不按完成矩阵 Terminal。失败 JSON：读循环不得自己关连接；按 §4.1 外层模板 `CancelTurn` + 视 `CancelResult` 写 `stage3_backpressure` + `terminalLocked`。

### 4.15 限额

conn_permit：非 Created/Stopped → 409 不 Acquire；不足 429。只在 Phase C 且 `permit_held` 释放。Stopping 仍占额度。  
speak_permit：仅 CAS 成功路径 Acquire；拷贝失败从未 Acquire。`terminalLocked` once 释放。

### 4.16 HTTP 媒体、stream、Turn 世系

禁止 HTTP 读任意服务器路径。禁止 raw PCM 上传。

- `POST /assets`：仅 WAV（RIFF PCM fmt）。超 `max_asset_bytes` 或 duration 超 `max_asset_duration_sec` → 400。
- `POST /devices/{id}/speak`：WAV 的 sample_rate/channels/sample_format 必须等于设备当前 `audio_*`，否则 400。
- `stream`：元素个数 ≤ `max_stream_entries`（默认 16）。各 audio 段 duration 与各 `silence.duration_ms` 之和 ≤ `max_stream_duration_sec * 1000`（默认 60s），否则 400。`silence` 为内部全零 PCM，格式同 `audio_fp`，禁止拼接 RIFF。
- 下载：`Content-Type: audio/wav`。CLI `--audio` 仅 Phase 1。
- **Turn / frames / audio：** 查询参数 **必填** `instance_id`。路由 §4.2。缺 → 400。只返回该 instance 的 Turn。tombstone TTL 内且文件仍在 → 200；文件缺失 → 404。禁止只凭 `device_id`+`turn_id` 在多个 instance 目录里搜索。

## 5. 硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text，禁止 UTF-8 解码下行音频 |
| AudioHeader | 100 字节含 padding；golden 对照基线 |
| 占用 | 先拷贝再 CAS Reserved |
| Stage=4 | 先 vad（若空），停 Stage=1，补 Stage=2 |
| 打断 | CancelTurn；禁止 BeginClose |
| 关连接 | 仅 BeginClose |
| Terminal | 只经 terminalLocked；appendEventLocked 摘 HTTP waiter 并投入 WS inbox；解锁后只唤醒 HTTP；禁止解锁 fan-out；禁止解锁路径关 WS |
| Stage=3 收口 | 单一队列；数据 `len>=depth-1` 拒；Stage=3 `len>=depth` 拒；depth>=2；CancelResult 走外层模板 acc；CloseResult 走 Phase A 模板 acc；BeginClose 的 len 取 keep |
| WS | close_mode=open\|drain\|abort；inbox 只 close 一次；写出 now+T；空闲独立 timer 1T；禁止 SetReadDeadline；Close 唤醒 reader |
| 心跳 | 周期 report；last_activity |
| 读循环 | 不因写盘阻塞 |
| 热更新 | playingMode 只经 Ready 的 report |
| Register | 先登记 timer 再发送；一次性消费 |
| Report | 解锁后再 enqueue；首序号=start |
| 收口 | 三段；Phase C 调 terminalLocked 一次并 409 唤醒 wait_ready |
| 游标 | 省略 ≡ 0；必填 instance_id |
| Turn/录音 | 必填 instance_id |
| 线上格式 | pcm s16le mono |

## 6. 音频管线

源 WAV 解 RIFF → 内部 PCM → silence=全零 → 按 slice_ms 切 pcm 字节。禁止逐片封装 WAV。Phase 4 才允许线上 mp3/wav 推流。

## 7. 技术选型

硬门槛：按字节收 TextMessage 二进制。Phase 1 推荐 Go。真实 core 收到至少一帧 `'0'` TTS 且未因 UTF-8 失败后锁定。

## 8. 阶段

| 阶段 | 交付 |
|------|------|
| Phase 1 | 单设备 CLI：握手→register→report→pcm 上行→回复；落盘；keepalive |
| Phase 2 | 批量+API+Scenario；playingMode 热更新；JSON ACK；WAV |
| Phase 3 | UI 只消费 Phase 2 |
| Phase 4 | speak backlog、非 pcm、静默探针 |

## 9. 目录

`protocol/` `core/` `cmd/speak|check|fixture/` `configs/example_device.yaml` `configs/manager.yaml` `configs/templates/` `testdata/` `data/assets/`

## 10. 对齐基线

仓库 `C:\Users\xie_f\projects\other\ai-creates-wealth` 提交 `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。  
冒烟与 golden **只对该提交的树**，不要混未提交改动。不要用 `projects/go/ai-creates-wealth`。register Redis 出错可不 ACK。管理消息独立 goroutine。MH Seq 例外。
