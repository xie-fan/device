# Phase 2 详细设计：多设备编排 + API + Scenario（v15）

阶段边界不变：本阶段才交付 **批量 + REST/WS + Scenario + JSON ACK + playingMode 热更新 + WAV**。  
不交付 Phase 4 **speak backlog**。错误码写死。

## 1. 目标

模板错峰、原子限额、可实施 HTTP/WS、浏览器可播下载、Scenario 在 speakable 之后才说话。

## 2. 范围与非目标

**范围：** Manager；permit + 代际新泵；join-wait finalizer；批量；wait_ready/speakable；先拷贝后 CAS；tombstone；字段 allowlist；JSON ACK；查询。

**非目标：** UI；speak backlog；线上非 pcm；HTTP raw PCM；Mongo；改 device_id；全局事件总线。

## 3. 错误码

| HTTP | 何时 |
|------|------|
| 400 | 缺字段、WS 缺 `device_id`、非 WAV、`asset_id` 与 `stream` 同时出现或都缺、stream 超限、PUT `device_id`/`write_queue_*` |
| 404 | 无 live 设备 / 无 turn / 无 asset / 无录音 / tombstone 未命中 |
| 409 | 重复 start；槽占用；`not_speakable`；身份/音频等 Running 时 PUT；`instance_mismatch`；批量创建 ID 冲突 |
| 410 | `after_event_seq < evicted_through_seq` |
| 429 | conn/speak permit |
| 503 | outbound buffer 满且尚未成功响应 |
| 504 | wait / wait_ready / speak_and_wait / Scenario wait_ready 超时 |
| 207 | 批量部分成功 |

## 4. Device Manager

```text
Created → Starting → Running → Stopping → Stopped → Deleted（tombstone）
```

speakable：正常 Running+Ready；skip_register Running+Connected；skip_report Running+Registered。否则 speak **409** `not_speakable`。

GET 含 `instance_id`、`instance_state`、`connection_state`、`conn_generation`、`last_activity`、`playing_mode`、`last_error`。

**start：** 仅 Created/Stopped。成功则 generation++、permit_held、**new writePump + 新会话字段复位**（见架构 §4.12）。HTTP **202**，**不等** Ready。Stopping 再 start → 409。

**Created 无连接：**

| 操作 | 结果 |
|------|------|
| stop | → Stopped，200，无 `connection_stopped`，无 finalizer |
| delete | tombstone + `device_deleted`，200，无 permit 释放 |

Starting/Running/Stopping 的 stop/delete：wait finalizer。Stopped 再 stop 幂等 200。Stopped 再 delete：仅 `device_deleted`。

## 5. Manager YAML

```yaml
manager:
  max_connections: 32
  max_concurrent_speaking: 8
  default_stagger_ms: 50
  per_device_buffer_bytes: 1048576
  write_queue_depth: 256          # outbound buffer，非 speak backlog
  write_drain_timeout_sec: 2
  event_log_max_entries: 10000
  event_log_ttl_hours: 24         # tombstone TTL 相同
  assets_root: "./data/assets"
  max_asset_bytes: 10485760
  max_asset_duration_sec: 60
  max_stream_entries: 16
  max_stream_duration_sec: 60
  wait_ready_timeout_sec: 30
```

设备 YAML 禁止 `write_queue_*`。

## 6. API

`{id}` = `device_id`。创建返回 `instance_id`。

### 6.1 资产

`POST /assets` multipart 字段 `file`，仅 WAV。201 含 fmt 元数据。DELETE 204；未拷贝进 Turn 前文件消失 → speak 404 且 **不 CAS**。

### 6.2 创建 / 模板

`POST /devices` 单台或 `template_id+count+id_prefix`。ID 冲突整批 409。201：`{ "device_ids", "instances": [ { "device_id", "instance_id" } ] }`。删除后同一 `device_id` 可再建（新 `instance_id`）。

`POST /templates` 禁止 `device_id` 与 `write_queue_*`。

### 6.3 生命周期

`POST /devices/{id}/start` → 202 `{ device_id, instance_id, conn_generation }`。

`POST /devices/{id}/wait_ready` 无 body，或 `POST /wait` `{ "device_id", "event_type": "speakable", "timeout_sec": null }`。已 speakable 立即 200；否则同锁登记。超时 504。默认 timeout = `wait_ready_timeout_sec`。

`POST /devices/{id}/stop`：按 §4 表。有连接则 wait `finalize_done`。200 时 Created 路径已是 Stopped。

`DELETE /devices/{id}`：按 §4 表。`device_deleted` 写入 **摘键前** 的 instance 日志，再 tombstone。200 `{ device_id, instance_id, deleted: true }`。

### 6.4 批量

`POST /devices/batch/start|stop|delete`：`{ device_ids, stagger_ms }`。  
HTTP start **202/207** 仍 **不等** Ready。stop/delete 对 Created 按 §4 计成功。

### 6.5 speak（互斥样例）

同时传 `asset_id` 与 `stream`，或两个都缺 → **400**。

仅资产：

```json
{ "asset_id": "ast_01HZX" }
```

仅时间轴：

```json
{
  "stream": [
    { "type": "audio", "asset_id": "ast_01HZX" },
    { "type": "silence", "duration_ms": 1500 }
  ]
}
```

顺序：**拷贝 PCM → 再检查 speakable → CAS + permit**。拷贝失败：404/400，槽空、无 permit。非 speakable：409，PCM 丢弃。

202：`{ turn_id, uplink_uuid, seq_before, instance_id }`。

`speak_and_wait` 另可选 `timeout_sec`（预算含 `post_final_asr_silence`）。

### 6.6 wait / WS / 查询

`POST /wait`：`device_id` 必填；`turn_id` 或 `event_type`。`speakable` 见 §6.3。游标规则见架构 §4.2。可选 `instance_id`（mismatch → 409）。

WS：`GET /ws/events?device_id=<必填>&instance_id=&turn_id=&after_event_seq=`。缺 device_id → 400。live `instance_id` 不符 → 409。

`GET /devices/{id}/events?instance_id=&after_event_seq=`：live 优先；删除后 tombstone 回放（含 `device_deleted`）。

`PUT /devices/{id}/config`：字段表见架构 §4.11。Running 改 `playing_mode` → 409，须 `POST /report`。

录音 GET：`audio/wav`。路径建议含 `instance_id` 以免重用 ID 串目录：`recordings/{device_id}/{instance_id}/{turn_id}/`。

`POST /report`：Ready 热更 `playingMode`。`POST /faults`：仅 Created/Stopped。

### 6.7 Scenario

`batch_start` **默认 `wait_ready: true`**（阻塞到 speakable）。可写 `"wait_ready": false` 仅当下一步显式 `wait_ready`。

```json
{
  "name": "batch-hello",
  "steps": [
    { "action": "batch_start", "device_ids": ["sim_1", "sim_2"], "stagger_ms": 50 },
    { "action": "speak", "device_id": "sim_1", "asset_id": "ast_01HZX", "wait": true },
    {
      "action": "assert",
      "device_id": "sim_1",
      "event_type": "tts_done",
      "turn_id": "$prev.turn_id",
      "after_event_seq": "$prev.seq_before"
    }
  ]
}
```

不要无游标重复 wait `turn_terminal`。`wait: true` 用含 silent 的预算。

202 `{ run_id }`。`GET /scenarios/runs/{run_id}` 含每步 `turn_id`、`seq_before`、`error`。

## 7–8. ACK 与 fault

JSON ACK 见架构 §4.10。A/B/C 同一 device_type。skip_register 新建后 Running+Connected 可 speak。

## 9. 验收

### 9.1 交付

WAV 上传、speak 互斥 body、wait_ready、WS、`audio/wav`、热更 playingMode、批量 207、tombstone 回放 `device_deleted`。

### 9.2 正确性

- [ ] Scenario：`batch_start` 返回后设备已 speakable；Starting 时 REST speak 为 409
- [ ] 同时给 `asset_id`+`stream` → 400
- [ ] 拷贝中 DELETE 资产 → 404 且无 Reserved、permit 不增
- [ ] stop 后再 start：新 generation、新泵能发 register；旧 closing 泵废弃
- [ ] finalizer 期间迟到 TTS 不先 Terminal；无「Terminal 后 Stage 3」
- [ ] Created stop→Stopped 无 connection_stopped；Created delete 仅 device_deleted
- [ ] 删除后重建：新 instance_id，旧 cursor + 新 live → 不串世系（mismatch 409）
- [ ] Running PUT sample_rate / playing_mode → 409；Ready report 热更 playingMode 成功
- [ ] join-wait、游标 0/oldest-1/oldest-2、用户停机事件：仍须通过
