# 玩具设备模拟器 — 整体架构设计文档（v15）

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：

1. **忠实模拟** 真实玩具设备的 chatbot 协议行为
2. **批量设备并发测试**（模板、错峰、限额）
3. 参数可配置、可保存
4. **Agent 可编程 API**（REST + 事件 WS）
5. **人工调试 UI**（配置 + 简单对话）

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。主路径：`Action=chatbot`。

**每阶段做完即可交付使用。** Phase 1 的交付物是能跑通的单设备 CLI。

## 2. 设计原则

1. **协议忠实**：对齐协议与基线 `types.AudioHeader`。`example/asr/mock.go` 仅缺陷清单，禁止作 golden。
2. **失败按 fault 矩阵推断**，禁止伪造服务端日志原因名。
3. **Turn 一等公民**：拆分 `uplink_end_reason` / `turn_end_reason`；先写不改。
4. speak 受理时 CAS Reserved；**先拷贝音频再 CAS**。
5. **API 先于 UI**。
6. Phase 1/2 线上 **pcm**（mono s16le）。HTTP 上传只接受 WAV。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK **只看报文** `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变；删除后允许重用，用 `instance_id` 隔离事件世系。
10. 推荐 Go。

已关闭且必须保留：提前下行只缓存、report 解锁后再 enqueue、register 先登记再发送且一次性消费、仅 IsFinal silent、限额 generation + once、`/wait` 同锁、`turn_terminal`、三段 finalizer join-wait、`BeginClose`、permit 在 close 之后、`evicted_through_seq`、正常停机事件、WAV、WS 必填 `device_id`。

本版补：Scenario/HTTP **speakable 屏障**、speak 互斥样例、先拷贝后 CAS、**每代新 writePump**、`finalize_started` 禁止完成矩阵、Created stop/delete、tombstone、PUT 字段 allowlist。

运输层缓冲称 **outbound buffer（writePump）**。Phase 4 才有 **speak backlog**（Turn 排队）。二者不是同一功能。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 模板批量 / stagger / conn_permit+speak_permit
                live 表 + tombstone（instance_id）
DeviceInstance（一次 instance 生命）:
  每代 ConnectionSession：新 websocket + 新 writePump
  request_finalize join-wait / BeginClose(finalFrame)
  keepalive report + last_activity
  pending_reports / early_downlink_buf / event_log（instance 级 seq）
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{instance_id}/{turn_id}/
assets/{asset_id}.wav
```

## 4. 核心抽象

### 4.1 Turn、speakable、先拷贝后 CAS

```text
Turn
├── turn_id / uplink_uuid
├── state     # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── uplink_frozen
├── injected_fault
├── uplink_end_reason   # 空 | stage2 | interrupt | vad | error | timeout；非空不改
├── turn_end_reason     # idle | interrupt | error | timeout | connection_lost
├── reply_kind
├── related / early_downlink_buf
├── first_reply_timer / settle_timer
├── pcm 拷贝（受理前完成）
```

- 每设备一槽。默认 reject → 409。
- **WaitingReply 之前**禁止因 command / 成功 JSON / 匹配 TTS 将 Turn 置 Terminal。
- `uplink_end_reason`：Stage=4 立即 `vad`（若空）再补 Stage=2；非空不改。Reserved 取消保持空。

**speakable 谓词（speak 前置，唯一答案）：**

| 路径 | 要求 |
|------|------|
| 正常 | `instance_state==Running` 且 `Connection==Ready` |
| skip_register | Running 且 Connected |
| skip_report | Running 且 Registered |

不满足 → **409** `not_speakable`，body 含 `instance_state`、`connection_state`。**包括 Starting / Stopping / Created / Stopped。** 不是 429、不是 404。

**受理顺序（禁止先占槽再读文件）：**

1. 校验 body：`asset_id` 与 `stream` **互斥且必须择一**；同时出现或都缺 → **400**。
2. **无锁（或短锁只查路径）** 把 WAV **拷贝进内存 PCM**（stream 则逐条拷贝；任一条失败立即停）。文件消失/不匹配设备音频参数 → **404/400**，此时 **尚未** CAS、**尚未** Acquire `speak_permit`。
3. 持 `device_mu`：再检查 speakable；CAS Reserved；成功路径内 TryAcquire speak_permit；失败则丢弃 PCM 并 409/429（permit 失败不得留下 Reserved）。
4. 同锁登记 `seq_before`、挂 PCM、speak_and_wait 的 waiter。
5. 解锁后由上行协程经 outbound buffer 发送。

**进入 Terminal 的唯一路径**（`device_mu`，与 `/wait` 同一把锁；不得持 `conn_mu` 做 IO）：

1. 写快照 → 释放 speak_permit（turn_id once）→ 释放槽 → append `turn_terminal` → 复制 waiter → **解锁后再唤醒**。

**例外：** `finalize_started==true` 后，完成矩阵 **不得** 把该 Turn 置 Terminal（见 §4.12）。迟到 TTS/command 仍可记事件/录帧，但 **不** 启动/重置 idle、followup、silent 以进入 Terminal。

**上行冻结：** Stage=1/2 入队前检查 `uplink_frozen` 或 Terminal 或 Connection ∈ {Disconnecting, Disconnected}。

### 4.2 Event、instance_id、tombstone

每次 `POST /devices` 成功分配 **`instance_id`**（UUID）。`device_id` 删除后可重用；**`event_seq` 属于 instance，从 1 起。** 新旧实例禁止共用游标。

每条事件含 `device_id`、`instance_id`、单调 `event_seq`。连接级用 `correlation_id`。Reserved 之后对话事件用 `turn_id`。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
| `ack_failure` | `/register/client` 且 `data.code != 0` |
| `report_echo` / `report_echo_unmatched` / `report_timeout` | report 回显 |
| `connection_failed` | 仅异常收口；Phase C 之后；带 reason |
| `connection_stopped` | 用户主动收口且 **本趟实际关了连接** |
| `device_deleted` | delete 摘 live 表 **之前** 写入本 instance 日志的最后一条 |
| `asr_result` / `command_received` / `json_reply` / `tts_chunk` / `tts_done` / `vad` | 对话 |
| `expected_server_drop` | 仅矩阵 drop 行作为通过 |
| `protocol_error` / `early_downlink_overflow` | 协议/缓冲 |
| `turn_terminal` | 槽已释放；各终态一律发送 |

**event_log：** 每 instance 一份。10000 条或 24h。`evicted_through_seq` 规则同前版实施：过期 iff `after_event_seq < evicted_through_seq`。省略游标只等未来。

**删除与回放：**

1. 若有活动连接：finalizer wait 到 Stopped。
2. 持锁 append `device_deleted`；把该 instance 的 log + `evicted_through_seq` 移入 **tombstone**（TTL = `event_log_ttl_hours`）。
3. 从 live 表摘键。已连接的 WS 推送 `device_deleted` 后关闭。
4. `GET /devices/{id}` → **404**。`GET /devices/{id}/events?instance_id=`：live 不匹配则查 tombstone；命中则回放至（含）`device_deleted`；`instance_id` 与 live 不一致 → **409** `instance_mismatch`。
5. 再 `POST /devices` 同一 `device_id`：新 `instance_id`，seq 从 1。不得读取上一世系的 `after_event_seq` 当成本世系历史。

WS：`device_id` **必填**（缺则 400）。可选 `instance_id`：与 live 不符 → 409（tombstone 仅用于 HTTP 回放，不升级 WS）。

### 4.3 `/wait`、speakable、speak_and_wait

`device_mu` 保护 Turn、event_log、waiter。

`POST /wait`：`device_id` 必填。`turn_id`、`event_type` 至少一个。

- `event_type=speakable`：同锁检查谓词，已满足立即 200；否则登记 waiter，Running 映射达成时唤醒。Starting 等到超时 → 504。
- 其它 `event_type`：游标过期 410；历史命中立即 200；否则登记。
- `turn_id`：无则 404；已 Terminal 立即 200。

禁止读完再解锁再注册。

### 4.4 完成矩阵

基线可不下发 TTS。关联、缓冲、计时与终止表：

**关联：** TTS 看 UUID；command/成功 JSON 占用；asr_result 看 SessionID+IsFinal；失败 JSON 立即停上行。

首次收到：事件、按需 ACK、录帧、落盘；未 WaitingReply 则入 `early_downlink_buf`（32，溢出丢最旧）。WaitingReply 前不 Terminal（失败 JSON 除外）。回放只改计时器，禁止二次 ACK/事件/录帧/落盘。

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态下行取消；默认 20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| 非音频 followup | command/成功 JSON 且尚无 TTS | 默认 5s；其后有 TTS 则改 idle |
| post_final_asr_silence | first_reply 到期且 IsFinal=true 且无终态 | 默认 5s → silent。interim-only 不得启动 |

终止均发 `turn_terminal`。仅 TTS → `tts_done`；仅 command/JSON 无 `tts_done`；仅 IsFinal → silent；仅 interim/全无 → timeout/drop；失败 JSON → error。

**`finalize_started` 后本表不生效**（不进入 Terminal）。

等待预算：`upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。

### 4.5 Scenario

TTS：`tts_done`。纯指令：`command_received`，不要 assert `tts_done`。捕获：`turn_id`、`seq_before`。禁止 `wait:true` 后再无游标等 `turn_terminal`。

**就绪：** Scenario 的 `batch_start` **默认 `wait_ready: true`**：每台阻塞到 speakable 或超时（504 该步失败）。HTTP `POST .../batch/start` **仍 202、不等待**。Agent 用 REST 时必须另发 `wait`/`wait_ready`。

### 4.6 注入矩阵

不查 Mongo。skip_register 夹具 `sim_sr_{run_uuid}_{n}` 新建，写 `fresh_ids.jsonl`。

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1 | drop |
| oversize | payload>51200 | drop |
| bad_header | `'0'` + 少于 100 字节 | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

### 4.7 取消表

| 状态 | 优雅关闭 `finalFrame` | 空的 uplink_end_reason | turn_end_reason |
|------|----------------------|------------------------|-----------------|
| Reserved | 无 | 保持空 | interrupt / connection_lost |
| Speaking | Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | Stage=3 | 保持 | 同上 |
| WaitingReply | Stage=3 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

Disconnecting：公开 Enqueue 拒绝。Stage=3 只经 BeginClose。写出 Stage 3 **之前** 再持锁看 Turn：若已 Terminal 或 `finalFrame` 已撤销 → **丢掉该帧**。异常关闭 `finalFrame=无`。

### 4.8 report 取号

首序号 = `report_sequence_start`。`report_mu` 内取号登记，**解锁后再** Enqueue。仅 initial 匹配回显 → Ready。新 generation **清空 pending**，序号复位为 `report_sequence_start`（新连接 initial report）。

### 4.9 outbound buffer（writePump）与 BeginClose

每 **ConnectionSession** 一个 writePump（**outbound buffer**，默认深度 256）。不是 Phase 4 speak backlog。

Phase 1：深度在设备 YAML。Phase 2：只在 Manager YAML；设备配置出现这两项 → 400。

**`BeginClose(finalFrame)`** 只作用于 **当前 session 的泵**：置 `closing=true`；优雅则丢未写 Stage 1/2 并追加 Stage 3；异常则丢队列。Phase B drain/关 socket。写出 Stage 3 前按 §4.7 复核。

旧泵 closing 后 **废弃**。下一趟 start **new writePump()**，不得把旧泵 `closing` 拨回 false。

Enqueue 满 → `request_finalize_async(write_backpressure)`；HTTP 未响应则 503。写失败 → write。泵协程禁止回锁 manager/device/conn。

### 4.10 ACK

Phase 1：binary SleepMs=0。Phase 2：json、非零 SleepMs、A/B/C。只看报文标志。字段：音频 Ack=Seq；指令 DownlinkType=3 / JSON topic=原指令。A/B/C 同一 device_type。

### 4.11 配置字段 allowlist

`device_id` 永不改（400）。

| 字段 | Created/Stopped PUT | Starting/Running/Stopping PUT | Ready `POST /report` |
|------|---------------------|-------------------------------|----------------------|
| enterprise, device_type | 200 | **409** | — |
| playing_mode | 200（只写入配置，供下次 start） | **409**（禁止与线上分叉） | **唯一热更路径** |
| audio.*（format/rate/channels/sample_format/slice_ms/max_payload） | 200 | **409** | — |
| server.url, uuid.*, action, firmware, nic_* | 200 | **409** | — |
| downlink_ack.* | 200 | **409** | — |
| behavior 超时 / keepalive / report_sequence_start / auto_register\|report | 200 | **409** | — |
| recording.* | 200 | 200 | — |
| write_queue_depth / write_drain_timeout_sec | **400** | **400** | — |

PUT `playing_mode` 在 Running/Ready **不得** 200。热更必须 report，且仅 Ready。

### 4.12 锁、register、代际复位、finalizer、Created 生命周期

**锁顺序：** `manager_mu` → `device_mu` → `conn_mu` → `report_mu`。持锁禁止 drain/close/wait `finalize_done`/调 `request_finalize`。

**start（仅 Created 或 Stopped）：**

持锁：非这两态 → 409 不 Acquire。TryAcquire 失败 → 429。成功则：

1. `conn_generation++`；`permit_held=true`；Starting。
2. **新建 ConnectionSession：** 新 websocket 计划、**新 writePump**（`closing=false`、空 outbound buffer）、`finalize_started/committed=false`、新 `finalize_done`、`winning_reason` 空、`uplink_frozen=false`、清空 pending 与 early_downlink、取消残留 timer、Turn 槽必须为空（否则 409，不得 start）。
3. 解锁后握手。失败只对 **该 generation** finalizer。

**register：** 锁内 Registering + attempt + settle + timer，解锁后 Enqueue。ACK/timeout `try_consume`；失败路径解锁后 `request_finalize_async`。`skip_register` 不发送、停 Connected。

**Running 映射：** 正常 Ready；skip_register Connected；skip_report Registered。GET 含 `instance_id`、`conn_generation`。

**`request_finalize`：** 后来者 join `finalize_done`。HTTP stop/delete **wait**。读/写/timer **async**，leader **只启动 closer 协程** 跑 B/C（禁止读循环自己等自己）。

reason 优先级：`user_delete` > `user_stop` > 先到异常。

Phase A：`finalize_started=true`；Stopping；Disconnecting；Turn 未 Terminal 则 `uplink_frozen` 并算 `finalFrame`；**完成矩阵关闭**；BeginClose；快照；解锁。不释放 permit。

Phase B：closer drain/close/等读循环。迟到下行不得 Terminal。

Phase C：Disconnected；若 Turn 仍非 Terminal → 取消表（不再出站）§4.1，`turn_end_reason=connection_lost`；释放 permit；Stopped；用户 winning → `connection_stopped` 且清空 `last_error`；异常 → `connection_failed`。broadcast `finalize_done`。

**Created / Stopped 无连接时（无 generation、无 permit）：**

| 操作 | 行为 | 事件 | HTTP |
|------|------|------|------|
| stop @ Created | → Stopped，不调 finalizer | 无 `connection_stopped` | 200 |
| stop @ Stopped | 幂等 | 无新事件 | 200 |
| delete @ Created 或 Stopped | tombstone + 摘键 | 仅 `device_deleted` | 200 |
| stop/delete @ Starting/Running/Stopping | join-wait finalizer，delete 再 tombstone | 见上 | 200 |

Created 的 stop/delete **不是 409**。批量同此。

### 4.13–4.14 keepalive 与读循环

Ready 后 60s report；`last_activity`；防 360s 踢线。读循环不阻塞落盘。`finalize_started` 后读循环仍可读，但完成矩阵不 Terminal。

### 4.15 限额

限额 429；冲突 409。Stopping 仍占 conn_permit。speak_permit 仅 CAS 成功路径；拷贝失败从未 Acquire。

### 4.16 REST 媒体

HTTP 仅 WAV。speak：**先拷贝后 CAS**。`asset_id` xor `stream`。stream 上限 16 条 / 总时长 60s。DELETE 不影响已拷贝的 Turn。下载 `audio/wav`。CLI `--audio` 仅 Phase 1。

## 5. 硬约束

按字节收包；头 100B；CAS Reserved；Stage=4 先 vad；心跳 report；读循环不堵；playingMode 只经 report 热更；register 一次性消费；report 解锁再 enqueue；出站经 outbound buffer；收口三段+新泵；等待预算含 silent；字段 allowlist；speakable 409。

## 6–7. 音频与选型

WAV → 内部 PCM → 切片。推荐 Go。

## 8. 阶段（边界不变）

| 阶段 | 交付 |
|------|------|
| Phase 1 | 单设备 CLI 闭环；outbound buffer；无 REST |
| Phase 2 | 批量+API+Scenario；WAV；JSON ACK |
| Phase 3 | UI 只消费 Phase 2 |
| Phase 4 | **speak backlog**、非 pcm、静默探针 |

## 9. 目录

`protocol/` `core/` `cmd/speak|check|fixture/` `configs/example_device.yaml` `configs/manager.yaml` `configs/templates/` `testdata/` `data/assets/`

## 10. 对齐基线

`C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。不要用 `projects/go/ai-creates-wealth`。
