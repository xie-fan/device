# Phase 2 详细设计：多设备编排 + API + Scenario（v17）

阶段边界不变。交付批量 + REST/WS + Scenario + JSON ACK + playingMode 热更新 + WAV。不交付 speak backlog。  
本文件写全方法路径、限额绑定、tombstone 与 wait_ready 唤醒。不引用其它版本目录。

## 1. 目标

Agent 按本节表即可实现客户端。Scenario 等到 speakable。终态字段以下文 §6.7 终止表为准（与同目录 architecture.md §4.4 同一张表，此处写全）。

## 2. 范围与非目标

**范围：** Manager；permit；代际新泵；join-wait；批量；wait_ready；先拷贝后 CAS；tombstone；allowlist；JSON ACK；查询与录音下载。

**非目标：** UI；speak backlog；非 pcm；raw PCM；Mongo；改 device_id；全局事件总线。

## 3. 错误码

| HTTP | 何时 |
|------|------|
| 400 | 缺 device_id/instance_id；wait_ready 缺 conn_generation；非 WAV；asset_id 与 stream 同时或都缺；stream 超 max_stream_entries / max_stream_duration_sec；WAV 与设备 audio_* 不符；PUT device_id 或 write_queue_* |
| 404 | `GET /devices/{id}` 已摘 live；events/wait/WS 的 instance_id 在 live 与 TTL 内 tombstone 都未命中；无 turn/asset/录音 |
| 409 | 重复 start；槽占用；not_speakable；generation_gone；audio_config_changed；generation_changed；Running PUT 身份/音频/playing_mode；批量创建冲突 |
| 410 | after_event_seq < evicted_through_seq |
| 429 | conn_permit / speak_permit |
| 503 | outbound buffer 满且尚未成功响应 |
| 504 | wait 超时且该 generation **尚未** finalize_committed |
| 207 | 批量部分成功 |

## 4. Manager

```text
Created → Starting → Running → Stopping → Stopped → Deleted（tombstone）
```

speakable：正常 Running+Ready；skip_register Running+Connected；skip_report Running+Registered。否则 speak 409 `not_speakable`。

start 仅 Created/Stopped；HTTP 202 不等 Ready；新 writePump。  
Created stop → Stopped，无 connection_stopped。Created/Stopped delete → 仅 device_deleted。Starting/Running/Stopping 的 stop/delete wait finalizer。

GET `/devices/{id}` 字段：device_id、instance_id、instance_state、connection_state、conn_generation、last_activity、playing_mode、last_error。摘 live 后该接口 **404**（即使 tombstone 仍在 TTL 内）。历史事件用 `GET /devices/{id}/events?instance_id=`，TTL 内 **200**。

## 5. Manager YAML

```yaml
manager:
  max_connections: 32
  max_concurrent_speaking: 8
  default_stagger_ms: 50
  per_device_buffer_bytes: 1048576
  write_queue_depth: 256
  write_drain_timeout_sec: 2
  event_log_max_entries: 10000
  event_log_ttl_hours: 24
  assets_root: "./data/assets"
  max_asset_bytes: 10485760
  max_asset_duration_sec: 60
  max_stream_entries: 16
  max_stream_duration_sec: 60
  wait_ready_timeout_sec: 30
```

设备 YAML 禁止 write_queue_depth / write_drain_timeout_sec。

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
`GET /devices/{id}` 单台 live。已从 live 摘除 → **404**（不把 tombstone 当设备资源）。TTL 内旧 `instance_id` 的事件回放见 §6.8，是 200 不是 404。

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
2. 命中 tombstone → **409** `generation_gone`（不是 404，也不是把游标接到新 live）。
3. 命中 live：该 `conn_generation` 已 `finalize_committed` → **409** `generation_gone`（**无论曾否 speakable**）。
4. live 当前 generation 与请求不同 → 409 `generation_gone`。
5. 已 speakable 且 generation 匹配且未 committed → **200** `{ "device_id","instance_id","conn_generation","connection_state" }`。
6. 否则登记 waiter。Phase C 置 `finalize_committed` 后必须摘该键 waiter，解锁后 **409 唤醒**。超时仅当 waiter 仍在表中且尚未 committed 才 **504**。新一代 Ready **不得** 唤醒上一代 waiter。

`POST /devices/{id}/stop`、`DELETE /devices/{id}` 按 §4。DELETE 200 `{ "device_id","instance_id","deleted": true }`。

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
speak_and_wait 200 另含终态：`turn_end_reason`、`uplink_end_reason`、`reply_kind`、`event_seq`、`event_type`（通常 `turn_terminal`）。取值见 §6.7 终止表。504 时 Turn 继续。

### 6.7 wait 与 WS

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

speakable 时另必填 `conn_generation`。  
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

**终止表（wait / speak_and_wait / GET turn 回这些字段）：**

| 路径 | reply_kind | turn_end_reason | 额外 |
|------|------------|-----------------|------|
| 仅 TTS idle | tts | idle | tts_done |
| 仅 command | command | idle | 无 tts_done |
| 仅成功 JSON | json | idle | 无 tts_done |
| command+TTS | command+tts | idle | tts_done |
| JSON+TTS | json+tts | idle | tts_done |
| 仅 IsFinal 无终态 | silent | idle | 无 tts_done |
| 仅 interim 或全无（正常） | 空 | timeout | 无 expected_server_drop |
| 同上且 fault 为 drop 行 | 空 | timeout | expected_server_drop |
| 失败 JSON | 空 | error | protocol_error |
| interrupt | 保持或空 | interrupt | |
| 连接收口 Phase C | 保持或空 | connection_lost | |

`GET /ws/events?device_id=<必填>&instance_id=<必填>&turn_id=&after_event_seq=`  
缺 ID → 400 不升级。TTL 内旧 instance_id → **升级并回放 tombstone**（含 `device_deleted` 后关闭），禁止接到新 live 的 seq。过期游标 410 不升级。

### 6.8 查询与录音

| 方法 | 路径与要点 |
|------|------------|
| GET | `/devices/{id}/config`（无 write_queue_*） |
| PUT | `/devices/{id}/config`（allowlist 见下表） |
| GET | `/devices/{id}/turns` |
| GET | `/devices/{id}/turns/{turn_id}` 含终态三字段 |
| GET | `/devices/{id}/turns/{turn_id}/frames` application/x-ndjson |
| GET | `/devices/{id}/turns/{turn_id}/audio/uplink` Content-Type: **audio/wav** |
| GET | `/devices/{id}/turns/{turn_id}/audio/downlink` Content-Type: **audio/wav** |
| GET | `/devices/{id}/events?instance_id=<必填>&after_event_seq=` TTL 内 tombstone **200** 回放到 device_deleted |
| POST | `/devices/{id}/interrupt` |
| POST | `/devices/{id}/report` body 可选 `{ "playingMode": 1 }`；Ready；202 `{ "sequence_number" }` |
| POST | `/devices/{id}/faults` `{ "fault": "skip_register" }`；仅 Created/Stopped |

磁盘：

```text
recordings/{device_id}/{instance_id}/{turn_id}/frames.jsonl
recordings/{device_id}/{instance_id}/{turn_id}/uplink.pcm
recordings/{device_id}/{instance_id}/{turn_id}/downlink.pcm
recordings/{device_id}/{instance_id}/{turn_id}/turn.json
```

GET audio 将 pcm 包成 WAV。无文件 404。

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

`batch_start` 默认 wait_ready。不要无游标重复 wait turn_terminal。

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

§6 全部方法可调用。浏览器：上传 WAV → POST speak → GET downlink audio/wav。

### 9.2 正确性

- [ ] 删除后 TTL 内：`GET /devices/{id}` → **404**；`GET .../events?instance_id=旧` → **200**（含 `device_deleted`）；不得接到新 live 的 seq
- [ ] TTL 外或未知 instance_id 的 events → 404
- [ ] wait_ready 命中 tombstone 或该代已 committed → **409** `generation_gone`，不是 404、不是 504
- [ ] wait_ready 登记后该代失败：Phase C **409 唤醒**；二代 Ready **不** 唤醒一代 waiter
- [ ] 短锁窗口内 DELETE 使 epoch 变 → speak 404，无 Reserved；拷贝完成后 DELETE 不影响该 Turn
- [ ] BeginClose 后未发 Stage=2 不会发出；Stage=3 为最后一帧
- [ ] `POST /devices/{id}/speak` 与 `POST /scenarios/run` 路径存在；超 max_stream_* 或 fmt 不符 → 400
- [ ] wait / speak_and_wait 200 的 reply_kind/turn_end_reason 符合 §6.7 终止表
- [ ] Created stop/delete、join-wait、游标 0/oldest-1/oldest-2 仍通过
