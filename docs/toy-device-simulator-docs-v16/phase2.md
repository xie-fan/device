# Phase 2 详细设计：多设备编排 + API + Scenario（v16）

阶段边界不变。本阶段原定目标仍是批量 + REST/WS + Scenario + JSON ACK + playingMode 热更新 + WAV。  
下文是 **完整可实施契约**（恢复列表/查询/录音 URL/batch 状态），并接上已有的 `instance_id`。不引入 speak backlog，不新开资源类型。

## 1–2. 目标与范围

Agent 能按表调用。Scenario 等到 speakable。拷贝有 epoch 线性化。

**非目标：** UI；speak backlog；非 pcm；raw PCM；Mongo；改 device_id；全局事件总线。

## 3. 错误码

| HTTP | 何时 |
|------|------|
| 400 | 缺 `device_id`/`instance_id`/`conn_generation`（wait_ready）、WS 缺字段、非 WAV、asset_id 与 stream 同时或都缺、PUT device_id/`write_queue_*` |
| 404 | 无精确匹配的 live/tombstone；无 turn/asset/录音 |
| 409 | 重复 start；槽占用；not_speakable；generation_gone；audio_config_changed；generation_changed；Running PUT 身份/音频/playing_mode；批量创建冲突 |
| 410 | after_event_seq < evicted_through_seq |
| 429 | permit |
| 503 | outbound buffer 满且未成功响应 |
| 504 | wait 超时（generation 仍活着） |
| 207 | 批量部分成功 |

## 4. Manager

Created→Starting→Running→Stopping→Stopped→Deleted(tombstone)。

speakable 见架构 §4.1。start 仅 Created/Stopped；**202 不等 Ready**；新 writePump。Created stop/delete 见架构 §4.12。

GET 字段：`device_id`、`instance_id`、`instance_state`、`connection_state`、`conn_generation`、`last_activity`、`playing_mode`、`last_error`。

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

## 6. 完整 API

`{id}` = `device_id`。创建返回 `instance_id`。

### 6.1 资产

`POST /assets` multipart 字段 `file`，仅 WAV。201：

```json
{ "asset_id": "ast_01HZX", "bytes": 32044, "duration_ms": 1000, "sample_rate": 16000, "channels": 1, "sample_format": "s16le", "container": "wav", "epoch": 1 }
```

`GET /assets/{asset_id}` 元数据。`GET /assets/{asset_id}/content` → `audio/wav`。  
`DELETE /assets/{asset_id}`：`epoch++` 后摘名，204。与拷贝的线性化见架构 §4.1。不提供服务器本地路径。

### 6.2 设备创建

单台：`POST /devices` `{ "device": { "enterprise":"demo","device_type":"A3","device_id":"sim_001","playing_mode":1 } }`  
批量：`{ "template_id":"default_a3","count":3,"id_prefix":"sim" }` → `{prefix}_{n}`；冲突整批 409。  
201：`{ "device_ids":["sim_1"], "instances":[ { "device_id":"sim_1","instance_id":"ins_..." } ] }`。删除后同 ID 可再建（新 instance_id）。

`GET /devices`：`{ "devices":[ { "device_id","instance_id","instance_state","connection_state","conn_generation","last_activity","playing_mode","last_error" } ] }`。查询可选 `instance_state`。  
`GET /devices/{id}`：单台同上；已删 404。

### 6.3 模板

`POST /templates`：

```json
{ "template_id":"default_a3", "device": { "enterprise":"demo","device_type":"A3","playing_mode":1,
  "audio":{ "format":"pcm","sample_rate":16000,"channels":1,"sample_format":"s16le","slice_ms":100,"max_payload_size":51200 } } }
```

禁止 `device_id`、`write_queue_*`。201 `{ "template_id" }`。落盘 `configs/templates/{template_id}.yaml`。  
`GET /templates` 列表。`GET /templates/{template_id}`。`DELETE /templates/{template_id}` 204。

### 6.4 生命周期

`POST /devices/{id}/start` 无 body。202：`{ "device_id","instance_id","conn_generation" }`。409 已在连；429 无 conn_permit。

`POST /devices/{id}/wait_ready` **有 body**（不是无 body）：

```json
{ "instance_id":"ins_...", "conn_generation": 3, "timeout_sec": null }
```

缺任一 ID/generation → 400。语义见架构 §4.3。200：`{ "device_id","instance_id","conn_generation","connection_state" }`。  
等价：`POST /wait` 加 `"event_type":"speakable"` 且同样必填字段。

`POST /devices/{id}/stop`：有连接则 wait finalize。Created→Stopped 无 connection_stopped。已 Stopped 幂等 200。  
`DELETE /devices/{id}`：按架构 §4.12。200 `{ "device_id","instance_id","deleted":true }`。

### 6.5 批量

`POST /devices/batch/start|stop|delete`

```json
{ "device_ids": ["sim_1","sim_2"], "stagger_ms": 50 }
```

相邻 start 间隔 ≥ stagger。响应：

```json
{
  "succeeded": [{ "device_id":"sim_1","instance_id":"ins_...","conn_generation":1 }],
  "failed": [{ "device_id":"sim_2","http_status":429,"error":"conn_permit_exhausted" }]
}
```

| 情况 | HTTP |
|------|------|
| 全部成功 | start **202**；stop/delete **200** |
| 部分成功 | **207** |
| 全失败且有 429 | **429** |
| 全失败且全 409 | **409** |
| 全失败其它 | **400** |

HTTP start **不等** Ready。Created 的 stop/delete 计入 succeeded。

### 6.6 speak

仅资产：`{ "asset_id":"ast_01HZX" }`  
仅时间轴：`{ "stream":[ { "type":"audio","asset_id":"ast_01HZX" }, { "type":"silence","duration_ms":1500 } ] }`  
两者都给或都缺 → 400。

先按 epoch 拷贝并快照 audio_fp/generation，再 CAS。202：`{ "turn_id","uplink_uuid","seq_before","instance_id" }`。  
`POST /devices/{id}/speak_and_wait` 另可选 `timeout_sec`。

### 6.7 wait 与 WS

`POST /wait`：

```json
{
  "device_id": "sim_001",
  "instance_id": "ins_...",
  "turn_id": "trn_...",
  "event_type": "turn_terminal",
  "after_event_seq": 0,
  "timeout_sec": null,
  "conn_generation": 3
}
```

`device_id`+`instance_id` 必填。`turn_id` 或 `event_type` 至少一个。`speakable` 时 `conn_generation` 必填。日志解析见架构 §4.2。  
200 含 `event_seq`、`event_type`、终态字段、`instance_id`。

`GET /ws/events?device_id=<必填>&instance_id=<必填>&turn_id=&after_event_seq=`  
缺任一 ID → **400 不升级**。精确选 live/tombstone。过期 410 不升级。

### 6.8 配置、Turn、录音（路径写死）

| 方法 | 说明 |
|------|------|
| `GET /devices/{id}/config` | 当前配置（无 write_queue_*） |
| `PUT /devices/{id}/config` | allowlist 见架构 §4.11 |
| `GET /devices/{id}/turns` | 摘要；查询当前 live instance |
| `GET /devices/{id}/turns/{turn_id}` | 完整 turn；404 无此 turn |
| `GET /devices/{id}/turns/{turn_id}/frames` | `application/x-ndjson` |
| `GET /devices/{id}/turns/{turn_id}/audio/uplink` | **`Content-Type: audio/wav`** |
| `GET /devices/{id}/turns/{turn_id}/audio/downlink` | **`Content-Type: audio/wav`** |
| `GET /devices/{id}/events?instance_id=<必填>&after_event_seq=` | 数组；tombstone 可回放到 `device_deleted` |
| `POST /devices/{id}/interrupt` | 200 在 `turn_terminal` 后；`finalize_started` 则不 Terminal |
| `POST /devices/{id}/report` | Ready 热更 playingMode；202 `{ "sequence_number" }` |
| `POST /devices/{id}/faults` | `{ "fault":"skip_register" }`；仅 Created/Stopped |

磁盘与 URL 对应：

```text
recordings/{device_id}/{instance_id}/{turn_id}/frames.jsonl
recordings/{device_id}/{instance_id}/{turn_id}/uplink.pcm
recordings/{device_id}/{instance_id}/{turn_id}/downlink.pcm
recordings/{device_id}/{instance_id}/{turn_id}/turn.json
```

GET audio 将 pcm 包成 WAV。无文件 404。

### 6.9 Scenario

`batch_start` 默认 wait_ready，使用返回的 `instance_id`+`conn_generation`。

```json
{
  "name": "batch-hello",
  "steps": [
    { "action": "batch_start", "device_ids": ["sim_1","sim_2"], "stagger_ms": 50 },
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

202 `{ "run_id" }`。`GET /scenarios/runs/{run_id}` 含 `instance_id`、`conn_generation`、`turn_id`、`seq_before`。

## 7–8. ACK 与 fault

架构 §4.10。A/B/C 同一 device_type。skip_register 新建后 Running+Connected 可 speak。

## 9. 验收

### 9.1 交付

§6 全表可调：列表、模板、turns/frames、两条 audio URL、batch 207/全失败码、WAV、wait_ready 带 generation、WS 双 ID。

### 9.2 正确性

- [ ] 缺 instance_id 的 wait/WS/events → 400；旧 instance_id + 新 live 同 device_id → 404（精确未命中），不把 after=12 接到新 seq
- [ ] wait_ready 绑 generation：一代失败后二代 Ready **不** 唤醒一代 waiter；一代 Stopped 未 speakable → 409 generation_gone
- [ ] 拷贝中 DELETE（epoch 变）→ 404 不占槽；拷贝完成后 DELETE 不影响该 Turn
- [ ] 拷贝中 PUT audio 再 start → 409 audio_config_changed，不把旧 PCM 挂新连接
- [ ] writePump 不取 device_mu；finalize_started 后仅 Phase C Terminal
- [ ] Created stop/delete、join-wait、游标 0/oldest-1/oldest-2 仍通过
