# Phase 2 详细设计：多设备编排 + API + Scenario（v14）

阶段边界不变：本阶段才交付 **批量 + REST/事件 WS + Scenario + JSON ACK + playingMode 热更新 + WAV 上传/录音下载**。  
Phase 1 已有的协议/CLI 不在此重做。错误码写死，不再「或」。

## 1. 目标

模板批量、错峰、原子限额、Agent 可实施的 HTTP/WS 契约、PCM 时间轴、查询与浏览器可播下载。

## 2. 范围与非目标

**范围：** Manager；conn/speak permit + 三段 finalizer **join-wait**；批量 start/stop/delete；生命周期；锁内 wait；Scenario 捕获；silence；`POST /report` 热更新 playingMode；JSON ACK 与 A/B/C；**仅 WAV** 资产；录音/frames 下载；查询/配置。

**非目标：** UI；queue；线上非 pcm；HTTP raw PCM；Mongo 预检；`sleep_ms:0` 清缓存；查 DownlinkAck；改 device_id；任意服务器路径读文件；全局事件日志。

## 3. 错误码（唯一答案）

| HTTP | 何时 |
|------|------|
| 400 | 缺字段（含 WS 缺 `device_id`）、非 WAV 资产、stream 超限、PUT 改 device_id、设备 YAML 含 `write_queue_*` |
| 404 | 无此设备 / 无此 turn / 无此 asset / 无录音 |
| 409 | 重复 start（含 Stopping）；槽占用（reject）；非 Created/Stopped 时改身份字段；批量创建 ID 冲突（整批） |
| 410 | `after_event_seq < evicted_through_seq` |
| 429 | conn_permit / speak_permit 不足 |
| 503 | writePump 队列满且请求尚未成功响应 |
| 504 | speak_and_wait / wait 超时（Turn **继续**） |
| 207 | 批量 start/stop/delete **部分成功** |

## 4. Device Manager

```text
Created → Starting → Running → Stopping → Stopped → Deleted
```

| 路径 | Starting→Running 当 | speak 要求 |
|------|---------------------|------------|
| 正常 | Connection=Ready | Running 且 Ready |
| skip_register | Connection=Connected | Running 且 Connected |
| skip_report | Connection=Registered | Running 且 Registered |

GET 返回 `instance_state`、`connection_state`、`conn_generation`、`last_activity`、`playing_mode`、`last_error`（仅异常收口非空）。

Stopping 期间 start → **409**。stop/delete **等到 Phase C**（后来者 wait `finalize_done`）再 200。

**conn_permit：** 锁内非 Created/Stopped → 409 且不 Acquire。Acquire 失败 429。成功 generation++、permit_held、Starting。释放只在 finalizer Phase C、socket 已关之后。

**speak_permit：** CAS 成功路径 Acquire；失败回滚。Terminal once 释放。

并发默认 reject（409）。可选 `cancel_previous`（先取消表再 CAS）。不交付 queue。

**身份字段：** 仅 Created/Stopped 可 PUT enterprise/device_type。Starting/Running/Stopping → 409。`device_id` 永不改。

## 5. Manager 配置 schema

`configs/manager.yaml`（进程启动加载）。**队列参数只在这里**；设备配置出现 `write_queue_depth` 或 `write_drain_timeout_sec` → 400。

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
```

## 6. API 契约

路径 `{id}` = 创建时 `device_id`。

### 6.1 资产（浏览器上传，仅 WAV）

`POST /assets`  
`Content-Type: multipart/form-data`，字段名 **`file`**。文件必须是 **WAV（RIFF PCM fmt）**。从 fmt 读 sample_rate、channels、sample_format；`duration_ms` 由 data 大小计算。raw PCM、缺 fmt、非法容器 → 400。写入 `assets_root/{asset_id}.wav`，拒绝 `..` 与绝对路径。超大小/时长 → 400。

201：

```json
{
  "asset_id": "ast_01HZX",
  "bytes": 32044,
  "duration_ms": 1000,
  "sample_rate": 16000,
  "channels": 1,
  "sample_format": "s16le",
  "container": "wav"
}
```

`GET /assets/{asset_id}`：元数据。`GET /assets/{asset_id}/content`：`Content-Type: audio/wav`（原文件）。  
`DELETE /assets/{asset_id}`：摘名 **204**。进行中的 speak 使用受理时拷贝的 PCM，不受删除影响。文件在拷贝前已消失 → speak 404。

不提供「按服务器本地路径读取」。

### 6.2 创建设备

`POST /devices`

单台：

```json
{ "device": { "enterprise": "demo", "device_type": "A3", "device_id": "sim_001", "playing_mode": 1 } }
```

批量：

```json
{ "template_id": "default_a3", "count": 3, "id_prefix": "sim" }
```

生成 `{id_prefix}_{n}`。任一 ID 已存在 → **整批 409**，一台都不创建。

201：`{ "device_ids": ["sim_1", "sim_2", "sim_3"] }`。

### 6.3 模板

`POST /templates`

```json
{
  "template_id": "default_a3",
  "device": {
    "enterprise": "demo",
    "device_type": "A3",
    "playing_mode": 1,
    "audio": { "format": "pcm", "sample_rate": 16000, "channels": 1, "sample_format": "s16le", "slice_ms": 100, "max_payload_size": 51200 }
  }
}
```

禁止在模板里写 `device_id` 以及 `write_queue_depth` / `write_drain_timeout_sec`。落盘 `configs/templates/{template_id}.yaml`。  
201：`{ "template_id": "default_a3" }`。  
`GET /templates`、`GET /templates/{template_id}`、`DELETE /templates/{template_id}`。

### 6.4 生命周期

`POST /devices/{id}/start` 无 body。  
202：`{ "device_id": "sim_001", "conn_generation": 3 }`。  
409 已 Starting/Running/Stopping；429 无 conn_permit。

`POST /devices/{id}/stop` 无 body。`reason=user_stop`。实现必须 `request_finalize(gen, user_stop)` 并 **wait `finalize_done`** 后再 200。若收口已由断线启动，stop **加入同一次** 并等到 Phase C。200 时已 Stopped；winning reason 按优先级（`user_delete` > `user_stop` > 先到异常）。用户侧 winning 时事件为 `connection_stopped`，`last_error` 为空。幂等：已 Stopped → 200，不 wait IO、不发第二次事件。

`DELETE /devices/{id}`：未 Stopped 则 `request_finalize(user_delete)` **wait 到 Phase C** 再摘键。200：`{ "device_id": "sim_001", "deleted": true }`。事件：`connection_stopped`（若本趟关了连接）然后 `device_deleted`，没有 `connection_failed`。已 Stopped 再 delete：只 `device_deleted`，不释放 permit。

### 6.5 批量 start/stop/delete

`POST /devices/batch/start|stop|delete`

```json
{ "device_ids": ["sim_1", "sim_2"], "stagger_ms": 50 }
```

省略 `stagger_ms` 则用 `default_stagger_ms`。相邻 **start** 间隔 ≥ stagger。stop/delete 同样错峰。每台独立走单台锁与 **wait 型** finalizer。

响应体：

```json
{
  "succeeded": [{ "device_id": "sim_1", "conn_generation": 1 }],
  "failed": [{ "device_id": "sim_2", "http_status": 429, "error": "conn_permit_exhausted" }]
}
```

HTTP 状态：

| 情况 | HTTP |
|------|------|
| 全部成功 | start **202**；stop/delete **200** |
| 至少一台成功且至少一台失败 | **207** |
| 全部失败，且存在 429 | **429** |
| 全部失败，且全为 409 | **409** |
| 全部失败，其它 | **400** |

### 6.6 speak

`POST /devices/{id}/speak`  
`Content-Type: application/json`。

```json
{
  "asset_id": "ast_01HZX",
  "stream": [
    { "type": "audio", "asset_id": "ast_01HZX" },
    { "type": "silence", "duration_ms": 1500 }
  ]
}
```

`asset_id` 与 `stream` 互斥，必有其一。silence 为内部 PCM 全零。未知 `asset_id` → 404。stream 条目 > `max_stream_entries` 或总时长 > `max_stream_duration_sec` → 400。WAV 与设备 `sample_rate`/`channels`/`sample_format` 不一致 → 400（上传时已有 fmt，speak 时再对齐设备）。

受理时（CAS 同锁）拷贝 PCM 进 Turn，并记录 `seq_before`。

202：`{ "turn_id": "trn_...", "uplink_uuid": 42, "seq_before": 11 }`。槽占用 409；speak_permit 尽 429。

`POST /devices/{id}/speak_and_wait`：字段同上，另可选 `timeout_sec`（省略用预算：含 `post_final_asr_silence`）。同一锁 CAS+waiter。200 同 §6.7 快照并含 `seq_before`。504 超时，Turn 继续。

### 6.7 `POST /wait`

```json
{
  "device_id": "sim_001",
  "turn_id": "trn_...",
  "event_type": "turn_terminal",
  "after_event_seq": 0,
  "timeout_sec": null
}
```

必填 `device_id`；`turn_id` 与 `event_type` 至少一个。`after_event_seq` 缺省表示只等未来。`0` 在 `evicted_through_seq=0` 时合法。同一设备锁检查+注册。

200：

```json
{
  "device_id": "sim_001",
  "turn_id": "trn_...",
  "event_seq": 12,
  "event_type": "turn_terminal",
  "turn_end_reason": "idle",
  "uplink_end_reason": "stage2",
  "reply_kind": "tts"
}
```

404 无 turn；410 游标过期；504 超时。

### 6.8 事件 WS

`GET /ws/events?device_id=<必填>&turn_id=&after_event_seq=`

- **`device_id` 必填**。缺省或空 → **HTTP 400，不升级**。event_seq / 淘汰水位都是该设备的日志。
- 可选 `turn_id`：只推该 Turn 相关事件。
- 省略 `after_event_seq` = 只推未来。若提供且 `< evicted_through_seq`：**HTTP 410，不升级**。body：`error=event_seq_expired`、`evicted_through_seq`、`oldest_seq`、`newest_seq`。

之后每条事件 JSON：`event_seq`、`type`、`device_id`、可选 `turn_id`。`turn_terminal` 必推。续传用最后 `event_seq` 作为新的 `after_event_seq`。不做全局序列、不跨设备混序。

### 6.9 查询、配置、录音播放

| 方法 | 说明 |
|------|------|
| `GET /devices` | 列表。查询：`instance_state`。200：`{ "devices": [ { "device_id", "instance_state", "connection_state", "conn_generation", "last_activity", "playing_mode", "last_error" } ] }` |
| `GET /devices/{id}` | 单台同上字段 |
| `GET /devices/{id}/config` | 当前设备配置（不含写队列字段） |
| `PUT /devices/{id}/config` | 改 device_id 400；改 enterprise/device_type 且非 Created/Stopped → **409**；body 含 `write_queue_*` 400 |
| `GET /devices/{id}/turns` | 摘要列表（含终态字段） |
| `GET /devices/{id}/turns/{turn_id}` | 完整 `turn.json` 等价对象 |
| `GET /devices/{id}/turns/{turn_id}/frames` | `application/x-ndjson` |
| `GET /devices/{id}/turns/{turn_id}/audio/uplink` | **`Content-Type: audio/wav`**（PCM+RIFF）；无文件 404 |
| `GET /devices/{id}/turns/{turn_id}/audio/downlink` | 同上，供 `<audio>` 直接播放 |
| `GET /devices/{id}/events?after_event_seq=` | 数组；过期 410 |
| `POST /devices/{id}/interrupt` | 无 body；200 在该 Turn 的 `turn_terminal` 之后 |
| `POST /devices/{id}/report` | `{ "playingMode": 1 }` 可选；Ready 才允许；202 `{ "sequence_number": 3 }` |
| `POST /devices/{id}/faults` | `{ "fault": "skip_register" }`；禁改 id；仅 Created/Stopped |

### 6.10 Scenario

`POST /scenarios/run`

speak 步骤在 CAS 同锁写入步骤结果：`turn_id`、`seq_before`。`wait: true` 等到该 Turn Terminal。后续引用：`$prev.turn_id`、`$prev.seq_before`。

**正确示例（无重复 wait）：**

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

`wait: true` 省略 timeout 则用含 `post_final_asr_silence` 的预算。纯指令不要 assert `tts_done`。  
禁止：`wait:true` 之后无游标再 `wait event_type=turn_terminal`。

若拆成异步 speak：

```json
{ "action": "speak", "device_id": "sim_1", "asset_id": "ast_01HZX", "wait": false },
{ "action": "wait", "device_id": "sim_1", "turn_id": "$prev.turn_id" }
```

第二条用 `turn_id` 查槽（已 Terminal 立即 200），不要省略游标去等 `event_type`。

202：`{ "run_id": "run_..." }`。  
`GET /scenarios/runs/{run_id}`：`{ "run_id", "status": "running|succeeded|failed", "steps": [ { "index", "status", "turn_id", "seq_before", "error" } ] }`。

## 7. ACK 与 report

JSON 字段见 `architecture.md` §4.10。A/B/C 同一 **device_type** 且 DownlinkAck=true，不是同一 ACK mode。

`POST /report` 与 keepalive 共用取号；payload 含当前 `playingMode`。热更后后续 report 带新值。

## 8. 故障注入

矩阵见 `architecture.md` §4.6。skip_register 夹具 **新建** 后 start → Running（Connected）可 speak。skip_report → Running（Registered）可 speak。

## 9. 验收

### 9.1 交付

- [ ] 按 §6 的 body/状态码：列表、配置、WAV 上传、speak(`asset_id`)、wait、WS（必填 device_id）、`audio/wav` 下载、热更 playingMode
- [ ] 浏览器：multipart WAV → speak → GET downlink 可在 `<audio>` 播放
- [ ] 批量创建、模板、错峰 start/stop/delete；207 部分成功
- [ ] 单台崩溃不影响其它

### 9.2 正确性

- [ ] 并发 stop 与断线：双方都等到同一 Phase C；HTTP stop 不提前 200
- [ ] delete 在 Stopping 中途到达：join 后 wait，再摘键
- [ ] `BeginClose` 后无新 Stage 1/2；优雅关闭仅一条 Stage=3
- [ ] drain 期间再 start 为 409；permit 在 close 之后才释放
- [ ] `post_final_asr_silence_sec` 大于 idle/followup 时 speak_and_wait 不 504
- [ ] Scenario：`wait:true` + 带 `seq_before` 的 assert 能看到 `tts_done`；无游标重复 wait 不得作为示例/实现
- [ ] 非 WAV 上传 400；缺 WS `device_id` 400
- [ ] Starting 时 PUT device_type → 409；Stopped 后可改
- [ ] 游标：`after=0`（oldest=1）200；`after=oldest-1` 200；`after=oldest-2`（已淘汰）410
- [ ] 用户主动收口为 `connection_stopped`，无 `last_error`
- [ ] JSON ACK 字段完整；A/B/C 同一 device_type
- [ ] 不可改 device_id
