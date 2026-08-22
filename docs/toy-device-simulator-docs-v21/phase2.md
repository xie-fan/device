# Phase 2 详细设计：多设备编排 + API + Scenario（v21）

阶段边界不变。交付批量 + REST/WS + Scenario + JSON ACK + playingMode 热更新 + WAV。不交付 speak backlog。  
本文件写全方法路径、interrupt、游标、录音世系。不引用其它版本目录。

## 1. 目标

Agent 按本节表即可实现客户端。Scenario 等到 speakable。终态字段以下文 §6.7 终止表为准（与同目录 architecture.md §4.4 同一张表，此处写全）。

## 2. 范围与非目标

**范围：** Manager；permit；代际新泵；join-wait；批量；wait_ready；先拷贝后 CAS；tombstone；allowlist；JSON ACK；查询与录音下载；CancelTurn；terminalLocked。

**非目标：** UI；speak backlog；非 pcm；raw PCM；Mongo；改 device_id；全局事件总线。

## 3. 错误码

| HTTP | 何时 |
|------|------|
| 400 | 缺 device_id/instance_id；wait_ready 缺 conn_generation；turns/frames/audio/interrupt 缺 instance_id；非 WAV；asset_id 与 stream 同时或都缺；stream 超 max_stream_entries / max_stream_duration_sec；WAV 与设备 audio_* 不符；PUT device_id 或 write_queue_* |
| 404 | `GET /devices/{id}` 已摘 live；events/wait/WS/turns/录音的 instance_id 在 live 与 TTL 内 tombstone 都未命中；无 turn/asset/录音文件；tombstone 上 `/wait` 历史未命中 |
| 409 | 重复 start；槽占用；not_speakable；generation_gone；audio_config_changed；generation_changed；Running PUT 身份/音频/playing_mode；批量创建冲突；interrupt 时 `finalize_started` / `turn_mismatch` |
| 410 | after_event_seq < evicted_through_seq |
| 429 | conn_permit / speak_permit |
| 503 | outbound buffer 满且尚未成功响应 |
| 504 | wait 超时且该 generation **尚未** finalize_committed；**不**用于冻结的 tombstone |
| 207 | 批量部分成功 |

无活动 Turn 的 interrupt **不是** 404，见 §6.8。

## 4. Manager

```text
Created → Starting → Running → Stopping → Stopped → Deleted（tombstone）
```

speakable：正常 Running+Ready；skip_register Running+Connected；skip_report Running+Registered。否则 speak 409 `not_speakable`。

start 仅 Created/Stopped；HTTP 202 不等 Ready；新 writePump。  
Created stop → Stopped，无 connection_stopped。Created/Stopped delete → 仅 device_deleted。Starting/Running/Stopping 的 stop/delete wait finalizer。

GET `/devices/{id}` 字段：device_id、instance_id、instance_state、connection_state、conn_generation、last_activity、playing_mode、last_error。摘 live 后该接口 **404**（即使 tombstone 仍在 TTL 内）。历史用带 `instance_id` 的 events / turns / audio。

## 5. Manager YAML

```yaml
manager:
  max_connections: 32
  max_concurrent_speaking: 8
  default_stagger_ms: 50
  per_device_buffer_bytes: 1048576
  write_queue_depth: 256   # 必须 >= 2
  write_drain_timeout_sec: 2   # 必须 > 0；设备泵 Phase B；事件 WS 单次写出 now+T，空闲探测总预算一个 T
  event_log_max_entries: 10000
  event_log_ttl_hours: 24
  assets_root: "./data/assets"
  max_asset_bytes: 10485760
  max_asset_duration_sec: 60
  max_stream_entries: 16
  max_stream_duration_sec: 60
  wait_ready_timeout_sec: 30
```

设备 YAML 禁止 write_queue_depth / write_drain_timeout_sec。Manager `write_queue_depth < 2` 或 `write_drain_timeout_sec <= 0` → 加载失败。

## 6. API

`{id}` = device_id。

### 6.1 资产

`POST /assets`  
Content-Type: multipart/form-data，字段名 `file`。仅 WAV。超 max_asset_bytes 或 max_asset_duration_sec → 400。201：

```json
{ "asset_id": "ast_01HZX", "bytes": 32044, "duration_ms": 1000, "sample_rate": 16000, "channels": 1, "sample_format": "s16le", "container": "wav", "epoch": 1 }
```

`GET /assets/{asset_id}` 元数据。`GET /assets/{asset_id}/content` → audio/wav。  
`DELETE /assets/{asset_id}`：短锁 epoch++ 并 unlink，204。拷贝：短锁读 path+epoch → 无锁读盘 → 短锁复验；epoch 变则 speak 404 且不 CAS。

### 6.2 设备

`POST /devices` 单台 `{ "device": { "enterprise":"demo","device_type":"A3","device_id":"sim_001","playing_mode":1 } }`  
或批量 `{ "template_id":"default_a3","count":3,"id_prefix":"sim" }`。ID 冲突整批 409。  
201：`{ "device_ids":["sim_1"], "instances":[ { "device_id":"sim_1","instance_id":"ins_..." } ] }`。

`GET /devices` → `{ "devices":[ { "device_id","instance_id","instance_state","connection_state","conn_generation","last_activity","playing_mode","last_error" } ] }`。  
`GET /devices/{id}` 单台 live。已从 live 摘除 → **404**。TTL 内旧 `instance_id` 的 events/turns/audio 见 §6.7–§6.8。

### 6.3 模板

`POST /templates`

```json
{ "template_id": "default_a3", "device": { "enterprise": "demo", "device_type": "A3", "playing_mode": 1,
  "audio": { "format": "pcm", "sample_rate": 16000, "channels": 1, "sample_format": "s16le", "slice_ms": 100, "max_payload_size": 51200 } } }
```

禁止 device_id 与 write_queue_*。201 `{ "template_id": "default_a3" }`。文件 `configs/templates/{template_id}.yaml`。  
`GET /templates`、`GET /templates/{template_id}`、`DELETE /templates/{template_id}`（204）。

### 6.4 生命周期

`POST /devices/{id}/start` 无 body。202 `{ "device_id","instance_id","conn_generation" }`。

`POST /devices/{id}/wait_ready` body：

```json
{ "instance_id": "ins_...", "conn_generation": 3, "timeout_sec": null }
```

缺字段 400。默认 timeout=`wait_ready_timeout_sec`。判定顺序不得颠倒：

1. 未命中 live 且未命中 TTL 内 tombstone → 404。
2. 命中 tombstone → **409** `generation_gone`。
3. 命中 live：该 `conn_generation` 已 `finalize_committed` → **409** `generation_gone`（**无论曾否 speakable**）。
4. live 当前 generation 与请求不同 → 409 `generation_gone`。
5. 已 speakable 且 generation 匹配且未 committed → **200** `{ "device_id","instance_id","conn_generation","connection_state" }`。
6. 否则登记 waiter。Phase C 置 `finalize_committed` 后必须摘该键 waiter，解锁后 **409 唤醒**。超时仅当 waiter 仍在表中且尚未 committed 才 **504**。新一代 Ready **不得** 唤醒上一代 waiter。

`POST /devices/{id}/stop`、`DELETE /devices/{id}` 按 §4。DELETE 200 `{ "device_id","instance_id","deleted": true }`。stop/delete 走 BeginClose（**过滤**），不走 CancelTurn。`cr=BeginClose` 走 architecture.md **Phase A 模板**：若 `backpressure` 则仍持 `device_mu` 时写入 `acc`，**进入 Phase B 之前** `notify(eventWaiters)`。§4.9 的 `len` 取 keep。若此前 `/interrupt` 或失败 JSON 已 `CancelTurn` 入队 Stage=3，BeginClose(token=无) **必须保留并写出** 该帧，禁止把 buffer 整队丢弃。删除时 `device_deleted` 同样累积 `acc`，解锁后唤醒 HTTP waiter；对事件 WS 走 `requestClose(drain)`，不得与 reader 失败共用「只 close inbox」。

### 6.5 批量

`POST /devices/batch/start`  
`POST /devices/batch/stop`  
`POST /devices/batch/delete`  
body：`{ "device_ids": ["sim_1","sim_2"], "stagger_ms": 50 }`。

响应：`{ "succeeded":[{ "device_id","instance_id","conn_generation" }], "failed":[{ "device_id","http_status","error" }] }`。  
全成功：start 202、stop/delete 200。部分成功 207。全失败有 429→429；全 409→409；其它 400。HTTP start 不等 Ready。

### 6.6 speak

`POST /devices/{id}/speak`  
`POST /devices/{id}/speak_and_wait`（另可选 `timeout_sec`）  
Content-Type: application/json。

仅资产：

```json
{ "asset_id": "ast_01HZX" }
```

仅时间轴：

```json
{ "stream": [ { "type": "audio", "asset_id": "ast_01HZX" }, { "type": "silence", "duration_ms": 1500 } ] }
```

同时给或都缺 → 400。  
`stream.length > max_stream_entries` → 400。  
各 audio 时长 + 各 silence.duration_ms 之和 > `max_stream_duration_sec * 1000` → 400。  
WAV fmt 必须等于该设备 audio.sample_rate / channels / sample_format，否则 400。  
silence：内部全零 PCM，同 audio_fp，禁止 RIFF。

受理：短锁快照 `audio_fp`/generation → 短锁读 path+epoch → **无锁读盘** → 短锁复验 epoch → 再加锁 CAS。拷贝窗口内 epoch 变 → 404，不占槽。

202：`{ "turn_id","uplink_uuid","seq_before","instance_id" }`。  
speak_and_wait 200 另含终态：`turn_end_reason`、`uplink_end_reason`、`reply_kind`、`event_seq`、`event_type`（通常 `turn_terminal`）。取值见 §6.7 终止表。504 时 Turn 继续。completion waiter 由 `terminalLocked` 摘出；同一临界区内各次 `appendEventLocked` 累积 HTTP event waiter；外层解锁后只唤醒 HTTP。WS 在锁内进入订阅 inbox，由该连接 writer 先写 backlog 切片再写 inbox，按 `event_seq` 升序。禁止调用方解锁后 fan-out。`CancelResult=backpressure` 必须在持锁时写入 `stage3_backpressure` 并入 `acc`。

### 6.7 wait 与 WS（游标全文）

省略 `after_event_seq` ≡ `after_event_seq=0`（从 oldest 起）。禁止「省略=只等未来」。只要未来：显式传当前 `newest_seq`。

`POST /wait`

```json
{
  "device_id": "sim_001",
  "instance_id": "ins_...",
  "turn_id": "trn_...",
  "event_type": "turn_terminal",
  "after_event_seq": 0,
  "timeout_sec": null
}
```

`device_id` 与 `instance_id` 必填。`turn_id` 或 `event_type` 至少一个。speakable 时另必填 `conn_generation`。

| 日志 | 省略或显式 after 后的行为 |
|------|---------------------------|
| live | 历史 `seq > after` 已命中 → 200；否则登记 **event waiter**。命中事件由 `appendEventLocked` 摘走，解锁后 200。禁止日志已有事件仍等到 504。超时仅当 waiter 仍在表中才 504（generation 未 committed） |
| tombstone | **只查冻结历史**。命中 → 200。未命中 → **404**，禁止 504，禁止登记 future waiter |

200：

```json
{
  "device_id": "sim_001",
  "instance_id": "ins_...",
  "turn_id": "trn_...",
  "event_seq": 12,
  "event_type": "turn_terminal",
  "turn_end_reason": "idle",
  "uplink_end_reason": "stage2",
  "reply_kind": "tts"
}
```

**终止表（wait / speak_and_wait / GET turn / interrupt 成功回这些字段）：**

| 路径 | reply_kind | turn_end_reason | 额外 | 泵 |
|------|------------|-----------------|------|-----|
| 仅 TTS idle | tts | idle | tts_done | 无 |
| 仅 command | command | idle | 无 tts_done | 无 |
| 仅成功 JSON | json | idle | 无 tts_done | 无 |
| command+TTS | command+tts | idle | tts_done | 无 |
| JSON+TTS | json+tts | idle | tts_done | 无 |
| 仅 IsFinal 无终态 | silent | idle | 无 tts_done | 无 |
| 仅 interim 或全无（正常） | 空 | timeout | 无 expected_server_drop | 无 |
| 同上且 fault 为 drop 行 | 空 | timeout | expected_server_drop | 无 |
| 失败 JSON | 空 | error | protocol_error | CancelTurn |
| interrupt | 保持或空 | interrupt | | CancelTurn |
| 连接收口 Phase C | 保持或空 | connection_lost | | BeginClose |

`GET /ws/events?device_id=<必填>&instance_id=<必填>&turn_id=&after_event_seq=`  
缺 ID → 400 不升级。过期游标 410 不升级。省略 after ≡ 0。

- live：持 `device_mu` 做 410 判定，拷贝 backlog 切片，空 inbox，`close_mode=open`，阶段 `catchup`，入 hub；解锁后升级，启动 writer 与 reader。writer 写完切片后 swap 到空且仍 `open` 才置 `live`。删除：`requestClose(drain)`，必须写出 `device_deleted`。reader：启动前设 `SetPongHandler`，只 `ReadMessage`；错误或 close 帧 `abort`。**禁止** `SetReadDeadline`。单次写出 `now+T`；空闲独立 timer，T/2 Ping，到 T 看 `last_pong` 续窗或 abort（`Close` 唤醒 reader）。禁止解锁 fan-out / 关 socket。
- tombstone：不入 hub。升级后由该连接单协程按序写出 backlog（含 `device_deleted`）再关闭。禁止空连接空等、禁止登记 future waiter。禁止接到新 live 的 seq。

### 6.8 查询、录音、interrupt（必填 instance_id）

Turn / frames / audio **禁止**只凭 `device_id`+`turn_id` 猜测世系。缺 `instance_id` → 400。路由与 events 相同：精确 live 或 TTL 内 tombstone。重建后新 instance 查旧 turn_id → 404。tombstone 命中但文件已删 → 404。

| 方法 | 路径与要点 |
|------|------------|
| GET | `/devices/{id}/config`（无 write_queue_*） |
| PUT | `/devices/{id}/config`（allowlist 见下表） |
| GET | `/devices/{id}/turns?instance_id=<必填>` 仅该 instance |
| GET | `/devices/{id}/turns/{turn_id}?instance_id=<必填>` 含终态三字段 |
| GET | `/devices/{id}/turns/{turn_id}/frames?instance_id=<必填>` application/x-ndjson |
| GET | `/devices/{id}/turns/{turn_id}/audio/uplink?instance_id=<必填>` Content-Type: **audio/wav** |
| GET | `/devices/{id}/turns/{turn_id}/audio/downlink?instance_id=<必填>` Content-Type: **audio/wav** |
| GET | `/devices/{id}/events?instance_id=<必填>&after_event_seq=` 省略 after ≡ 0；TTL 内 tombstone **200** 含 device_deleted |
| POST | `/devices/{id}/interrupt` 见下方 |
| POST | `/devices/{id}/report` body 可选 `{ "playingMode": 1 }`；Ready；202 `{ "sequence_number" }` |
| POST | `/devices/{id}/faults` `{ "fault": "skip_register" }`；仅 Created/Stopped |

磁盘：

```text
recordings/{device_id}/{instance_id}/{turn_id}/frames.jsonl
recordings/{device_id}/{instance_id}/{turn_id}/uplink.pcm
recordings/{device_id}/{instance_id}/{turn_id}/downlink.pcm
recordings/{device_id}/{instance_id}/{turn_id}/turn.json
```

GET audio 将 pcm 包成 WAV。无文件 404。录音文件 TTL 与 `event_log_ttl_hours` 相同。

**`POST /devices/{id}/interrupt`**  
Content-Type: application/json。body **必填** `instance_id`（必须等于当前 live）。可选 `turn_id`（若给则必须等于当前槽）。

```json
{ "instance_id": "ins_..." }
```

| 条件 | HTTP | body |
|------|------|------|
| 缺 instance_id | 400 | |
| instance_id 非当前 live（含 tombstone） | 409 `generation_gone` | |
| 可选 `turn_id` 已给但不是当前槽 | 409 `turn_mismatch` | 不 CancelTurn |
| `finalize_started==true` | 409 `finalize_started` | 不 CancelTurn、不 Terminal |
| 槽空或已 Terminal | **200** | `{ "interrupted": false, "device_id", "instance_id" }` |
| 有活动 Turn | `cr=CancelTurn`；若 `backpressure` 则 `appendEventLocked(stage3_backpressure)`；再 `terminalLocked(interrupt)`；**200 在解锁唤醒 HTTP waiter 之后**（WS 已在锁内入 inbox） | `{ "interrupted": true, "device_id", "instance_id", "turn_id", "turn_end_reason": "interrupt", "uplink_end_reason", "reply_kind" }` |

连接保持。禁止 BeginClose。失败 JSON 走同一条 CancelTurn 路径，无 HTTP。

**PUT allowlist**

| 字段 | Created/Stopped | Starting/Running/Stopping | Ready `POST /report` |
|------|-----------------|---------------------------|----------------------|
| enterprise, device_type | 200 | 409 | — |
| playing_mode | 200（写入配置，下次 start） | 409 | 唯一热更 |
| audio.* | 200 | 409 | — |
| server.url, uuid.*, action, firmware, nic_* | 200 | 409 | — |
| downlink_ack.* | 200 | 409 | — |
| behavior 超时/keepalive/report_sequence_start/auto_register\|report | 200 | 409 | — |
| recording.* | 200 | 200 | — |
| write_queue_depth / write_drain_timeout_sec / device_id | 400 | 400 | — |

### 6.9 Scenario

`POST /scenarios/run` body：

```json
{
  "name": "batch-hello",
  "steps": [
    { "action": "batch_start", "device_ids": ["sim_1", "sim_2"], "stagger_ms": 50 },
    { "action": "speak", "device_id": "sim_1", "asset_id": "ast_01HZX", "wait": true },
    {
      "action": "assert",
      "device_id": "sim_1",
      "instance_id": "$prev.instance_id",
      "event_type": "tts_done",
      "turn_id": "$prev.turn_id",
      "after_event_seq": "$prev.seq_before"
    }
  ]
}
```

202 `{ "run_id": "run_..." }`。  
`GET /scenarios/runs/{run_id}`：`{ "run_id","status":"running|succeeded|failed","steps":[ { "index","status","instance_id","conn_generation","turn_id","seq_before","error" } ] }`。

`batch_start` 默认 wait_ready。assert / wait 必须带 `after_event_seq`（通常 `$prev.seq_before`）。省略游标会从 oldest 回放，不要依赖「只等未来」。

## 7. ACK

运行时只看报文 `NeedAck==1` / `need_ack==1`。`sleep_ms: 0` 不写节流缓存、不能清旧值。

Phase 2 允许 json 与非零 SleepMs。

- 音频 binary：Ack=音频 Seq；DownlinkType TTS=1 / 提示音=2
- 音频 JSON：ack 与 sequence_number=音频 Seq；downlink_type=`tts`/`hint_audio`；uuid=音频 UUID
- 指令 binary：Ack=指令 sequence_number（缺省 0）；DownlinkType=**3**
- 指令 JSON：ack 与 sequence_number=指令序号；downlink_type=`command`；topic=原指令完整 topic

binary：`'4'`+28 字节。json：`'1'` + `.../downlink-ack/server`。

A/B/C：同一 **device_type** 且 DownlinkAck=true；三台不同 ID；status=1；TTS≥2 片。A=binary/0，B=binary/500，C=json/500。

## 8. 故障注入

不查 Mongo。skip_register：夹具 `sim_sr_{run_uuid}_{n}` 写入 `testdata/fixtures/fresh_ids.jsonl`，本趟不预注册，**新建**实例。新建后 Running+Connected 可 speak。skip_report：Running+Registered 可 speak。

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1 | drop |
| oversize | payload>51200 | drop |
| bad_header | `'0'` + 其后少于 100 字节 | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

## 9. 验收

### 9.1 交付

§6 全部方法可调用。浏览器：上传 WAV → POST speak → GET downlink `?instance_id=` audio/wav。

### 9.2 正确性

- [ ] `/interrupt` 发 Stage=3 后连接仍可 keepalive / 再 speak；**不** BeginClose
- [ ] 无活动 Turn interrupt → 200 `interrupted:false`；`finalize_started` → 409
- [ ] 失败 JSON CancelTurn，连接保持
- [ ] `terminalLocked` 不重复加锁；`appendEventLocked` 摘 HTTP waiter 并投入 WS inbox；同一临界区多次写事件须累积 `EventNotify`；`CancelResult=backpressure` 必须入 `acc`。禁止日志已有事件而 `/wait` 仍 504
- [ ] live WS：inbox 空且 `open` 才置 live；删除 `drain` 写出 `device_deleted`；reader 只 ReadMessage，禁止 SetReadDeadline。停读：卡住的那次 WriteMessage 在 T 内失败并 abort。半开空闲：独立 timer，T/2 Ping，从进入空闲起 ≤ T 无 Pong 则 writer abort 并 Close 唤醒 reader。Pong 成功后连接跨多个 T 窗口仍 live
- [ ] 省略 `after_event_seq` ≡ 0。tombstone WS 回放到 `device_deleted` 后关闭，不是空等。tombstone `/wait` 未命中 → 404 不是 504
- [ ] turns/frames/audio 缺 `instance_id` → 400。重建同 device_id 后，旧 instance_id 在 TTL 内可读旧录音；新 instance_id + 旧 turn_id → 404
- [ ] 删除后 TTL 内：`GET /devices/{id}` → **404**；`GET .../events?instance_id=旧` → **200**；`device_deleted` 的 `/wait` 为 200 不是 504
- [ ] wait_ready 命中 tombstone 或 committed → 409；Phase C 409 唤醒；二代 Ready 不唤醒一代
- [ ] 短锁窗口内 DELETE → speak 404；拷贝完成后 DELETE 不影响该 Turn
- [ ] BeginClose 是过滤不是清空：未发 Stage=2 不会发出；CancelTurn 已入队的 Stage=3 在随后 BeginClose(token=无) 时仍写出；§4.9 的 len 取 keep；`CloseResult=backpressure` 在 Phase A 解锁后、Phase B 前唤醒 HTTP waiter，禁止丢给 Phase C
- [ ] 单一队列：数据 `len >= depth-1` 拒绝；Stage=3 `len >= depth` 拒绝；`depth >= 2`。无「其它 uuid 占用控制槽」第三条规则。`/interrupt` 在 backpressure 时仍 200 且有 `stage3_backpressure` 事件
- [ ] `POST /devices/{id}/speak` 与 `POST /scenarios/run` 存在；超 max_stream_* 或 fmt 不符 → 400
- [ ] Created stop/delete、join-wait、游标 0/oldest-1/oldest-2 仍通过
