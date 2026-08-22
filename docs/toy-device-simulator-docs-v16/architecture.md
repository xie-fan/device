# 玩具设备模拟器 — 整体架构设计文档（v16）

## 1. 背景与目标

玩具项目通过 WebSocket 提供 ASR → LLM → TTS。模拟器用于：忠实协议、批量并发、可保存配置、Agent REST+WS、调试 UI。

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。主路径 `Action=chatbot`。  
Phase 1 交付物是能跑通的单设备 CLI。

## 2. 设计原则

1. 协议忠实；mock.go 禁止作 golden。
2. 失败按 fault 矩阵推断；禁止伪造服务端日志名。
3. Turn 一等公民；`uplink_end_reason` 先写不改。
4. speak：**先拷贝再 CAS**。
5. API 先于 UI。
6. Phase 1/2 线上 pcm；HTTP 仅 WAV。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK 只看报文 `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变；删除后可重用，事件用 **必填** `instance_id` 隔离。
10. 推荐 Go。

已关闭项须保留（完整性，不是新功能）：提前下行只缓存、report 解锁后 enqueue、register 一次性消费、仅 IsFinal silent、限额 once、`/wait` 同锁、`turn_terminal`、三段 join-wait、BeginClose、permit 在 close 之后、`evicted_through_seq`、正常停机事件、WAV、speakable、先拷贝后 CAS、代际新泵、Created stop/delete、tombstone、PUT allowlist。

运输层：**outbound buffer（writePump）**。Phase 4 才有 **speak backlog**。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 模板 / stagger / permit / live + tombstone[instance_id]
DeviceInstance:
  每代 ConnectionSession = 新 WS + 新 writePump
  BeginClose(stage3_token)  # 泵内原子，不回锁 device
  event_log（instance 级 seq）
recordings/{device_id}/{instance_id}/{turn_id}/
assets/{asset_id}.wav + epoch
```

## 4. 核心抽象

### 4.1 Turn、speakable、拷贝线性化

状态：Reserved | Speaking | FinishingUpload | WaitingReply | Terminal。每设备一槽。WaitingReply 前禁止因 command/成功 JSON/TTS 而 Terminal。

`uplink_end_reason`：Stage=4 立即 vad（若空）再补 Stage=2；非空不改。Reserved 取消保持空。

**speakable：** 正常 Running+Ready；skip_register Running+Connected；skip_report Running+Registered。否则 **409** `not_speakable`（含 Starting）。

**资产 epoch：** 每个 asset 有单调 `epoch`（创建=1）。`DELETE` 在 `asset_mu` 下 `epoch++` 并摘文件（线性化点）。拷贝：同锁读 `epoch` 与全部字节；读完再读 `epoch`，不一致或文件已无 → **404**，不 CAS。拷贝完成后 DELETE 只摘名，**不影响**已在内存的 PCM。

**音频配置快照：** 拷贝前短持 `device_mu` 记录 `audio_fp`（sample_rate/channels/sample_format）与 `conn_generation`（无连接则为 0），立即解锁再读盘。CAS 前再持锁：`audio_fp` 变了 → **409** `audio_config_changed`；`snap_gen!=0` 且 `snap_gen!=当前 conn_generation` → **409** `generation_changed`；然后 speakable → CAS → speak_permit。拷贝/校验失败路径 **均不** 占槽、不 Acquire。

`asset_id` xor `stream`；同时给或都缺 → 400。stream 任一条失败即停。

**Terminal 路径（`finalize_started==false` 时）：** 完成矩阵 / interrupt / 失败 JSON 等，持 `device_mu`：快照 → 释 speak_permit → 释槽 → `turn_terminal` → 解锁后唤醒。

**`finalize_started==true` 之后，进入 Terminal 的唯一路径是 finalizer Phase C。** 完成矩阵、`POST /interrupt`、失败 JSON 的「立即 Terminal」均 **禁止** 再置 Terminal（下行仍可记事件/录帧，不计时进入 idle）。speak CAS → 409。因此 **不存在**「Phase B 中 Turn 已 Terminal 仍要发 Stage 3」；**禁止** writePump 为复核 Turn 而回锁 `device_mu`/`conn_mu`。

### 4.2 Event、游标全文、tombstone

`POST /devices` 分配 `instance_id`。`event_seq` 从 1、按 instance。每条事件含 `device_id`、`instance_id`、`event_seq`。连接级 `correlation_id`。对话事件在 Reserved 后带 `turn_id`。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
| `ack_failure` | `/register/client` 且 `data.code != 0` |
| `report_echo` / `report_echo_unmatched` / `report_timeout` | report 回显 |
| `connection_failed` | 仅异常收口；Phase C 之后 |
| `connection_stopped` | 用户主动收口且本趟关了连接 |
| `device_deleted` | 摘 live **前** 写入该 instance 日志最后一条 |
| `asr_result` / `command_received` / `json_reply` / `tts_chunk` / `tts_done` / `vad` | 对话 |
| `expected_server_drop` | 仅矩阵 drop 行通过 |
| `protocol_error` / `early_downlink_overflow` | 协议/缓冲 |
| `turn_terminal` | 槽已释放；各终态一律发送 |

**event_log（写全，不引用其它版本）：**

- 每 instance 一份。保留 10000 条或 24h（先到为准）。
- `evicted_through_seq`：已丢弃的最大 seq；空且未淘汰时为 **0**。有条目时 `oldest_seq == evicted_through_seq + 1`。
- `after_event_seq` **排他**：返回/等待 `event_seq > after`。
- **410** `event_seq_expired` 当且仅当 `after_event_seq < evicted_through_seq`。body：`evicted_through_seq`、`oldest_seq`、`newest_seq`。只用 410。
- `after=0` 且 `evicted_through=0`（oldest=1）→ 不过期。`after == oldest_seq-1` → 不过期。`after == oldest_seq-2` 且已淘汰 → 410。
- **省略 `after_event_seq`：** 只等未来，不回放，不 410。仍须带 `instance_id`（见下）。

**解析日志（精确，无回退链）：**

`instance_id` 在 `/wait`、WS、`GET .../events` **必填**（与 `device_id` 一起）。缺 → **400**。

```text
若 live[device_id].instance_id == 请求的 instance_id → 用 live 日志
否则若 tombstone[instance_id] 存在且 tombstone.device_id == 请求的 device_id → 用 tombstone
否则 404
```

禁止「live 不匹配再搜 tombstone」。禁止省略 `instance_id` 时默认当前 live（否则 `after=12` 无法区分世系）。

删除：finalizer 如需则 wait → append `device_deleted` → log 移入 tombstone（TTL=`event_log_ttl_hours`）→ 摘 live。WS 推送后关闭。重建同 `device_id` 得 **新** `instance_id`、seq 从 1。

### 4.3 `/wait` 与 wait_ready

`device_mu` 同时保护 Turn、log、waiter。检查+注册同一临界区。

必填：`device_id`、`instance_id`。另需 `turn_id` 或 `event_type` 至少一个。

**`event_type=speakable` / `POST .../wait_ready`：** 必填 `conn_generation`。同锁：

- 日志按 §4.2 解析失败 → 404。
- 该 `conn_generation` 已 `finalize_committed` 或实例已 Deleted，且该代从未 speakable → **409** `generation_gone`（立即，不 504）。
- 当前 `conn_generation` 与请求不同 → **409** `generation_gone`（后来的 start 不得唤醒上一代 waiter）。
- 已 speakable 且 generation 匹配 → 立即 200。
- 否则登记 waiter；仅当 **同一** instance + generation 变为 speakable 时唤醒。超时 504。

`turn_id`：无此 Turn → 404；已 Terminal → 200。其它 `event_type`：游标过期 410；历史命中 200；否则登记。

`speak_and_wait`：CAS 同锁登记 completion waiter。

### 4.4 完成矩阵

关联：TTS UUID；command 已 Speaking 后；asr SessionID+IsFinal；成功 JSON 同 command；失败 JSON 立即停上行。

首次：事件、按需 ACK、录帧、落盘；未 WaitingReply 入 `early_downlink_buf`（32）。WaitingReply 前不 Terminal（失败 JSON 除外）。回放只改计时器，禁止二次 ACK/事件/录帧。

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态取消；20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| followup | command/JSON 尚无 TTS | 5s；其后 TTS 改 idle |
| post_final_asr_silence | first_reply 到期且 IsFinal 且无终态 | 5s→silent；interim-only 禁止 |

终止均 `turn_terminal`。仅 TTS 有 `tts_done`。`finalize_started` 后本表不 Terminal。

预算：`upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。

### 4.5 Scenario

TTS：`tts_done`。纯指令：`command_received`。捕获 `turn_id`/`seq_before`。禁止无游标重复 wait `turn_terminal`。

Scenario `batch_start` 默认 `wait_ready:true`：用 **该次 start 返回的** `instance_id`+`conn_generation` 等待 speakable。HTTP `batch/start` 仍 202 不等待。此为已有 wait_ready 的用法，不是新阶段功能。

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

### 4.7 取消表与 Stage 3 token

| 状态 | BeginClose token | 空 uplink_end_reason | turn_end_reason |
|------|------------------|----------------------|-----------------|
| Reserved | 无 | 保持空 | interrupt / connection_lost |
| Speaking | Stage3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；Stage3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | Stage3 | 保持 | 同上 |
| WaitingReply | Stage3 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

Phase A 已持 `device_mu`→`conn_mu` 时调用 `BeginClose`：只置泵内 `closing` 与 **原子 `stage3_token`**（有或无）。锁顺序允许 `device → conn → writePump_mu`。泵协程 **永不** 取 device/conn。Drain 写出 token 中的 Stage 3 **不再看 Turn**（见 §4.1 唯一 Terminal 证明）。异常关闭 token=无，丢 outbound buffer。公开 Enqueue 在 closing 时拒绝。

### 4.8 report

首序号=`report_sequence_start`。`report_mu` 取号后解锁再 Enqueue。仅 initial 回显 → Ready。新 generation 清空 pending，序号复位为 start。

### 4.9 outbound buffer

每 ConnectionSession 新 writePump。Phase 1 深度在设备 YAML；Phase 2 只在 Manager。满 → `request_finalize_async(write_backpressure)`，HTTP 未响应 503。旧泵废弃，禁止把 `closing` 拨回。

### 4.10 ACK

Phase 1：binary SleepMs=0。Phase 2：json、非零 SleepMs、A/B/C。  
音频 binary：Ack=音频 Seq；DownlinkType 1/2。指令 binary：Ack=指令序号；DownlinkType=3。  
JSON 音频：ack/sequence_number=音频 Seq；uuid=音频 UUID。JSON 指令：topic=原指令；uuid 省略或 0。  
A/B/C：同一 device_type，DownlinkAck=true，三台不同 ID，TTS≥2 片。

### 4.11 PUT allowlist

`device_id`、`write_queue_*` → 400。enterprise/device_type、audio.*、server、uuid、nic、firmware、action、downlink_ack、behavior 超时/keepalive/report_sequence_start：仅 Created/Stopped。Running PUT `playing_mode` → 409，热更只许 Ready 的 `POST /report`。recording.* 可 Running PUT。

### 4.12 锁、代际、finalizer、Created

锁顺序：`manager_mu` → `device_mu` → `conn_mu` → `report_mu`（→ 泵内锁仅由持 conn 的 Phase A 或仅泵协程单独持有）。持实例锁禁止 drain/wait finalize。

start：仅 Created/Stopped；否则 409 不 Acquire。成功 generation++、permit_held、**新 writePump**、复位 finalize_* / pending / uplink_frozen。槽非空不得 start。

register：锁内 timer+settle，解锁发送。ACK/timeout 一次性消费；失败 async finalize。读循环不得当 closer。

join-wait：HTTP stop/delete wait `finalize_done`。reason：`user_delete` > `user_stop` > 先到异常。

Phase A：`finalize_started`；Stopping；Disconnecting；冻结上行；完成矩阵关闭；BeginClose(token)；不释 permit。  
Phase B：closer drain/close。  
Phase C：Disconnected；若 Turn 非 Terminal → `connection_lost` 走 §4.1；释 permit；Stopped；事件；broadcast。

Created/Stopped 无连接：stop@Created→Stopped、无 `connection_stopped`；delete@Created/Stopped→仅 `device_deleted`。不是 409。

### 4.13–4.16

keepalive：Ready 后 60s report；`last_activity`；防 360s。读循环不阻塞落盘。限额 429。HTTP 仅 WAV。CLI `--audio` 仅 Phase 1。

## 5. 硬约束

按字节收包；头 100B；CAS Reserved；Stage=4 先 vad；心跳；playingMode 只经 report 热更；游标必填 instance_id；泵不回锁；拷贝 epoch+audio_fp；wait_ready 绑 generation。

## 6–8. 音频、选型、阶段

WAV→PCM 切片。推荐 Go。

| 阶段 | 交付 |
|------|------|
| Phase 1 | 单设备 CLI；outbound buffer |
| Phase 2 | 批量+API+Scenario；WAV；JSON ACK |
| Phase 3 | UI 只消费 Phase 2 |
| Phase 4 | speak backlog、非 pcm、探针 |

## 9–10. 目录与基线

`protocol/` `core/` `cmd/speak|check|fixture/` `configs/` `testdata/` `data/assets/`  
`C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。
