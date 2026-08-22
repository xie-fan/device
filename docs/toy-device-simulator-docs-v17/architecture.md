# 玩具设备模拟器 — 整体架构设计文档（v17）

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
9. `device_id` 创建后不可变；删除后可重用。事件用必填 `instance_id` 隔离。
10. 推荐 Go（按字节收 TextMessage 中的二进制）。

运输层缓冲称 **outbound buffer（writePump）**。Phase 4 才有 **speak backlog**。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 模板 / stagger / conn_permit / speak_permit / live + tombstone
DeviceInstance:
  每代 ConnectionSession = 新 WebSocket + 新 writePump
  BeginClose：清本 Turn 已排队 Stage 1/2 + 可选 Stage 3 token
  request_finalize 三段 + join-wait
  pending_reports / early_downlink_buf / event_log（instance 级）
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{instance_id}/{turn_id}/     # Phase 2
recordings/{device_id}/{turn_id}/                   # Phase 1 单进程
assets/{asset_id}.wav + epoch
```

## 4. 核心抽象

### 4.1 Turn 与占用

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

**Terminal（`finalize_started==false`）：** 持 `device_mu`（与 `/wait` 同一把）：写快照（含 `reply_kind`、`turn_end_reason`、`uplink_end_reason`）→ 释 speak_permit → 释槽 → append `turn_terminal` → 复制 waiter → **解锁后再唤醒**。禁止先唤醒再写 log。

**`finalize_started==true` 后，唯一 Terminal 路径是 Phase C。** 完成矩阵、interrupt、失败 JSON 均不得再置 Terminal。speak → 409。writePump **禁止**为看 Turn 而回锁 `device_mu`/`conn_mu`。

Stage=1/2 入队前检查：`uplink_frozen` 或 Terminal 或 Connection ∈ {Disconnecting, Disconnected} → 停止且不入队。

### 4.2 Event、游标、tombstone（唯一路由）

`POST /devices` 分配 `instance_id`。`event_seq` 从 1，属于该 instance。每条事件含 `device_id`、`instance_id`、`event_seq`。连接级用 `correlation_id`。Reserved 之后的对话事件带 `turn_id`。

禁止发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志名。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
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

**游标：**

- 每 instance 一份 log。10000 条或 24h（先到为准）。
- `evicted_through_seq`：已丢弃的最大 seq；从未淘汰为 0。有条目时 `oldest_seq == evicted_through_seq + 1`。
- `after_event_seq` 排他：`event_seq > after`。
- 410 `event_seq_expired` 当且仅当 `after < evicted_through_seq`。body：`evicted_through_seq`、`oldest_seq`、`newest_seq`。
- `after=0` 且 `evicted_through=0` → 不过期。`after == oldest-1` → 不过期。`after == oldest-2` 且已淘汰 → 410。
- 省略 `after_event_seq`：只等未来。仍须 `instance_id`。

**日志解析（仅用于 events / `/wait` 历史事件 / WS 回放。`wait_ready` 见 §4.3，命中 tombstone 是 409 不是 200）：**

`/wait`、WS、`GET .../events` 必填 `device_id` 与 `instance_id`。缺 → 400。

```text
按 instance_id 精确查找（不是「按 device_id 猜当前 live」）：
  若 live 表中该 device_id 的 instance_id 等于请求值
      → 使用 live 日志
  否则若 tombstone[instance_id] 存在
       且 tombstone.device_id 等于请求的 device_id
       且未超过 event_log_ttl_hours
      → 使用 tombstone 日志（200 回放，含 device_deleted）
  否则 → 404
```

因此：**TTL 内**用旧 `instance_id` 访问 **events / `/wait` 历史 / WS** 能读到删除前历史，**不会** 404，也 **不会** 读到重建后的新 seq。TTL 外或 ID 从未存在 → 404。  
**`GET /devices/{id}` 不走 tombstone：** 摘 live 后一律 404（设备资源已不存在；历史只通过带 `instance_id` 的事件接口回放）。  
重建同 `device_id` 得到新 `instance_id`，seq 从 1。

删除步骤：如需则 wait finalizer → append `device_deleted` → 整份 log 移入 tombstone → 摘 live。WS 推送该事件后关闭。

### 4.3 `/wait` 与 wait_ready

`device_mu` 保护 Turn、log、waiter、`speakable_waiters`。检查与登记同一临界区。禁止读完再解锁再注册。

必填 `device_id`、`instance_id`。另需 `turn_id` 或 `event_type`。

**实例解析（wait_ready / `event_type=speakable` 与事件回放共用 §4.2 路由，但 HTTP 语义不同）：**

| 命中 | `GET /devices/{id}` | `GET .../events` / `/wait` 历史事件 | `wait_ready` / `event_type=speakable` |
|------|---------------------|-------------------------------------|----------------------------------------|
| live 且 instance_id 匹配 | 200 | 200 读 live 日志 | 按下表判定 |
| TTL 内 tombstone 精确命中 | **404**（已摘 live） | **200** 回放至含 `device_deleted` | **409** `generation_gone` |
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
- Phase C 持锁：对本 generation 置 `finalize_committed=true`，**取出键匹配的全部** `speakable_waiters`，再办 Stopped/事件；**解锁后以 409 `generation_gone` 唤醒**。禁止只在 speakable/Ready 时唤醒。
- 超时回调：若 waiter 仍在表中 **且** 该 generation **尚未** `finalize_committed` → 摘掉并 **504**。若已被 Phase C 摘走或已 committed → **不得** 再 504（结果已是 409）。

其它 `event_type`：游标过期 410；历史命中 200；否则登记。`turn_id`：无 404；已 Terminal 200。

`speak_and_wait`：CAS 同锁登记 completion waiter。超时只摘 waiter，不 Terminal。

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

首次收到：事件、按需 ACK、录帧、落盘；未 WaitingReply 则入 `early_downlink_buf`（容量 32，溢出丢最旧并 `early_downlink_overflow`）。

WaitingReply 前：不启动完成计时、不 Terminal、不释槽（失败 JSON 除外）。回放只驱动计时器，禁止二次 ACK/事件/录帧/落盘。

**计时（仅 WaitingReply 起）**

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态下行取消；默认 20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| 非音频 followup | command/成功 JSON 且尚无 TTS | 默认 5s；其后有 TTS 则改 idle |
| post_final_asr_silence | first_reply 到期且已有 IsFinal=true 且无终态下行 | 默认 5s → silent。interim-only 不得启动 |

**终止（均发 `turn_terminal`，响应须带回这些字段）：**

| 路径 | 计时结束 | reply_kind | turn_end_reason | 额外事件 |
|------|----------|------------|-----------------|----------|
| 仅 TTS | TTS idle | tts | idle | tts_done |
| 仅 command | followup idle | command | idle | 无 tts_done |
| 仅成功 JSON | followup idle | json | idle | 无 tts_done |
| command 后 TTS | 改 TTS idle | command+tts | idle | tts_done |
| JSON 后 TTS | 改 TTS idle | json+tts | idle | tts_done |
| 仅 IsFinal、无终态下行 | silent idle | silent | idle | 无 tts_done |
| 仅 interim 或全无（正常路径） | first_reply 到期 | 空 | timeout | 无 expected_server_drop |
| 仅 interim 或全无（fault 矩阵 drop 行） | first_reply 到期 | 空 | timeout | expected_server_drop |
| 失败 JSON | 立即 | 空 | error | protocol_error |
| 用户 interrupt | 取消表 | 保持已有或空 | interrupt | 无 tts_done（除非此前已发） |
| 连接收口 Phase C | 取消表 | 保持已有或空 | connection_lost | 无 |

完全无包的静默成功与 drop 在正常路径不可区分：`timeout`。探针属 Phase 4。

`finalize_started` 后本表不进入 Terminal。

等待预算：`upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。禁止默认写死 30s。

### 4.5 Scenario

TTS 用例 assert `tts_done`。纯指令 assert `command_received`，不要 `tts_done`。文本 `json_reply`。捕获 `turn_id`、`seq_before`、`instance_id`。禁止 `wait:true` 后再无游标等 `turn_terminal`。

Scenario 的 `batch_start` 默认 `wait_ready:true`，使用本次 start 返回的 `instance_id` 与 `conn_generation`。HTTP `POST /devices/batch/start` 仍 202、不等待。

运行端点：`POST /scenarios/run`，查询：`GET /scenarios/runs/{run_id}`。`batch_start` 默认等到 speakable；HTTP `POST /devices/batch/start` 仍 202。body 与字段见同目录 `phase2.md` §6.9（该节写全 steps 样例）。

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

### 4.7 取消表与 BeginClose 清队列

| 状态 | 优雅关闭 | 空的 uplink_end_reason | turn_end_reason |
|------|----------|------------------------|-----------------|
| Reserved | 不发 Stage=3 | 保持空 | interrupt / connection_lost |
| Speaking | 停 Stage=1；发 Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 **未发出** | **不发** Stage=2；发 Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发出 | 发 Stage=3 | 保持 | 同上 |
| WaitingReply | 发 Stage=3；取消计时器 | 保持 | 同上 |
| Terminal | 无出站 | 不变 | 不变 |

套接字已死或异常关闭：不发 Stage=3。

**`BeginClose`（持 writePump_mu；调用方已持 device→conn，泵协程永不回锁）：**

1. `closing=true`。之后公开 Enqueue 失败（不二次 finalize）。
2. **优雅且 token=Stage3：** 从 outbound buffer **删除本 turn_id 所有尚未写出的 Stage=1 与 Stage=2**（因此未发出的 Stage=2 不会因 FIFO 先于 Stage=3 发出）。再 **清空** buffer 中其余帧（ACK/report 不再发送）。**追加一帧 Stage=3 作为本 ConnectionSession 最后一帧**（抢占：它是随后 drain 的唯一剩余载荷）。若某一帧 **已经从 buffer 取出并正在 `Write`**，视为已发出，不得半截撤回；随后仍只写出这一帧（若不是 Stage=3）再写出 token 中的 Stage=3。
3. **token=无（异常 / Reserved / 已 Terminal）：** 清空 outbound buffer，不追加 Stage=3。正在 `Write` 的那一帧可写完，其后不再取队列。
4. 返回。Phase B drain 直到空或 `write_drain_timeout_sec`，然后关 socket。

### 4.8 report

`report_next_seq` 初值 = `report_sequence_start`。第一个序号等于 start。  
`report_mu` 内：取号、递增、登记 pending。**解锁后再 Enqueue。** 仅 `kind=initial` 且 Reporting → Ready。新 generation 清空 pending，序号复位为 start。keepalive 与手动 report 共用。

### 4.9 outbound buffer

每代新 writePump。默认深度 256。Phase 1 在设备 YAML；Phase 2 只在 Manager YAML，设备配置出现则 400。满：`request_finalize_async(write_backpressure)`，HTTP 未响应则 503。写失败：`write`。旧泵废弃，禁止把 `closing` 拨回 false。

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

锁顺序：`manager_mu` → `device_mu` → `conn_mu` → `report_mu`。Phase A 可在持 conn 时取 `writePump_mu`。泵协程不得取 manager/device/conn。持实例锁禁止 drain、关 socket、wait `finalize_done`、调用 `request_finalize`。

**start：** 仅 Created 或 Stopped，否则 409 且不 TryAcquire。Acquire 失败 429。成功：`conn_generation++`、`permit_held=true`、Starting、**新 writePump**、`finalize_started/committed=false`、新 `finalize_done`、清空 pending 与 early_downlink、`uplink_frozen=false`。槽非空不得 start。

**register：** 持 conn_mu 置 Registering、`register_attempt_id`、settle=pending、启动 timer，**解锁后** Enqueue。ACK 与 timeout 做 `try_consume`；失败解锁后 `request_finalize_async`。`Timer.Stop` 不等待已开火 callback。skip_register 不发送、不装 timer，停 Connected。

**Running 映射：** 正常 Ready；skip_register Connected；skip_report Registered。

**request_finalize：** 后来者 join `finalize_done`。HTTP stop/delete **wait**。读/写/timer **async**。leader 只启动 **closer 协程** 跑 B/C。reason：`user_delete` > `user_stop` > 先到异常。

Phase A：`finalize_started=true`；Stopping；Disconnecting；`uplink_frozen`；完成矩阵关闭；按取消表设 token；BeginClose；不释 permit。  
Phase B：drain / 关 socket / 等读循环（时限 `write_drain_timeout_sec`）。  
Phase C（持锁，无 IO）：对本 generation 置 `finalize_committed=true`；Connection=Disconnected；Turn 若非 Terminal 则 `connection_lost` 走 §4.1；清空 pending；`permit_held` 则释放一个 conn_permit；Stopped；按 winning reason 写 `connection_failed` 或 `connection_stopped` 并处理 `last_error`；**取出本 generation 的全部 `speakable_waiters` 与 Turn waiter**（与超时互斥）；broadcast `finalize_done`；**解锁后** 以 409 `generation_gone` 唤醒 speakable waiter，再唤醒 Turn waiter。不得留给 504。

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

独立读协程持续读。指令 / ASR / TTS 头立即进事件。录帧与落盘异步。禁止在读协程同步写盘。`finalize_started` 后仍可读，但不按完成矩阵 Terminal。

### 4.15 限额

conn_permit：非 Created/Stopped → 409 不 Acquire；不足 429。只在 Phase C 且 `permit_held` 释放。Stopping 仍占额度。  
speak_permit：仅 CAS 成功路径 Acquire；拷贝失败从未 Acquire。Terminal once 释放。

### 4.16 HTTP 媒体与 stream 限额（配置绑定）

禁止 HTTP 读任意服务器路径。禁止 raw PCM 上传。

- `POST /assets`：仅 WAV（RIFF PCM fmt）。超 `max_asset_bytes` 或 duration 超 `max_asset_duration_sec` → 400。
- `POST /devices/{id}/speak`：WAV 的 sample_rate/channels/sample_format 必须等于设备当前 `audio_*`，否则 400。
- `stream`：元素个数 ≤ `max_stream_entries`（默认 16）。各 audio 段 duration 与各 `silence.duration_ms` 之和 ≤ `max_stream_duration_sec * 1000`（默认 60s），否则 400。`silence` 为内部全零 PCM，格式同 `audio_fp`，禁止拼接 RIFF。
- 下载：`Content-Type: audio/wav`。CLI `--audio` 仅 Phase 1。

## 5. 硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text，禁止 UTF-8 解码下行音频 |
| AudioHeader | 100 字节含 padding；golden 对照基线 |
| 占用 | 先拷贝再 CAS Reserved |
| Stage=4 | 先 vad（若空），停 Stage=1，补 Stage=2 |
| 心跳 | 周期 report；last_activity |
| 读循环 | 不因写盘阻塞 |
| 热更新 | playingMode 只经 Ready 的 report |
| Register | 先登记 timer 再发送；一次性消费 |
| Report | 解锁后再 enqueue；首序号=start |
| 出站 | 全部 outbound buffer；BeginClose 清 Stage 1/2 |
| 收口 | 三段；Phase C 置 committed 并 409 唤醒 wait_ready |
| 游标 | 必填 instance_id；TTL 内 tombstone 回放 |
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
