# Phase 2 详细设计：多设备编排 + API + Scenario（v12）

阶段边界不变：本阶段才交付 **批量 + REST/事件 WS + Scenario + JSON ACK + playingMode 热更新**。  
Phase 1 已有的协议/CLI 不在此重做。错误码写死，不再「或」。

## 1. 目标

Agent 可稳定调用的 HTTP/WS 契约；模板错峰；原子限额。

## 2. 范围与非目标

**范围：** Manager；conn/speak permit + finalizer；批量；生命周期 API（含完整 body）；锁内 wait；Scenario；silence；report 热更 playingMode；JSON ACK；查询。

**非目标：** UI；queue；线上非 pcm；Mongo 预检；改 device_id。

## 3. 错误码（唯一答案）

| HTTP | 何时 |
|------|------|
| 400 | 缺字段、body 非法、PUT 改 device_id |
| 404 | 无此设备 / 无此 turn_id |
| 409 | 重复 start（已 Starting/Running）；槽占用（reject）；Running 时改身份字段 |
| 410 | `after_event_seq` 已淘汰（`event_seq_expired`） |
| 429 | conn_permit / speak_permit 不足；批量 start 碰到连接上限 |
| 503 | writePump 队列满且请求尚未 202 |
| 504 | speak_and_wait / wait 超时（Turn **继续**） |

## 4. Manager 配置 schema

`configs/manager.yaml`（进程启动加载）：

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
```

Running 映射：正常 Ready；skip_register Connected；skip_report Registered。GET 含 `instance_state`、`connection_state`、`conn_generation`、`last_activity`、`playing_mode`。

**conn_permit：** 锁内若非 Created/Stopped → 409 **不 Acquire**。Acquire 失败 429。成功则 generation++、permit_held、Starting。释放 **只**在 `connection_finalizer`。stop 后 delete 不再释放。stop∥断线只 finalizer 一次。

**speak_permit：** CAS 成功路径 Acquire；失败回滚。Terminal once 释放。

## 5. API 契约

路径 `{id}` = 创建时 device_id。

### 5.1 `POST /devices`

单台 body：`device` 配置（可无 id 时用生成规则）。  
批量：`template_id`、`count`、`id_prefix` → `{prefix}_{n}`；冲突整批 409。

201：`{ "device_ids": ["..."] }`。

### 5.2 `POST /devices/{id}/start`

无 body。202：`{ "device_id", "conn_generation" }`。  
409 已在连；429 无 conn_permit。

`POST /devices/batch/start`：`{ "device_ids": [], "stagger_ms": 50 }`。每台独立走 start 锁。部分 429 时 body 列成功/失败。

### 5.3 `POST /devices/{id}/speak`

```json
{
  "audio": "testdata/hello.wav",
  "stream": [
    { "type": "audio", "file": "a.wav" },
    { "type": "silence", "duration_ms": 1500 }
  ]
}
```

`audio` 与 `stream` 互斥，必有其一。  
202：`{ "turn_id", "uplink_uuid" }`。槽占用 409；speak_permit 尽 429。  
音频在 CAS+登记（若 wait）成功后由后台 writePump 发送。

### 5.4 `POST /devices/{id}/speak_and_wait`

body 同 speak，另可选 `timeout_sec`（省略用预算公式）。  
同一锁：CAS + 注册 waiter。  
200：同 §5.5 的 Turn 快照。504：超时，Turn 继续。

### 5.5 `POST /wait`

```json
{
  "device_id": "sim_001",
  "turn_id": "可选",
  "event_type": "可选，如 turn_terminal",
  "after_event_seq": 0,
  "timeout_sec": null
}
```

必填 `device_id`；`turn_id` 与 `event_type` 至少一个。  
**同一设备锁：** 已 Terminal / 历史已命中 → 立即 200；否则登记 waiter。  
200：

```json
{
  "device_id": "sim_001",
  "turn_id": "...",
  "event_seq": 12,
  "event_type": "turn_terminal",
  "turn_end_reason": "idle",
  "uplink_end_reason": "stage2",
  "reply_kind": "tts"
}
```

404 无 turn；410 游标过期；504 超时。

### 5.6 事件 WS

`GET /ws/events?device_id=&turn_id=&after_event_seq=`  
握手查询串即订阅过滤。`after_event_seq` 缺省=只推未来。过期：握手失败 **410** 或首帧 JSON `{ "error": "event_seq_expired", "oldest_seq", "newest_seq" }`（选定：**HTTP 410 不升级**）。  
之后每条事件 JSON，字段含 `event_seq`、`type`、`device_id`、可选 `turn_id`。`turn_terminal` 必推以便 UI 知槽释放。  
续传：断开后用最后 `event_seq` 再连。

### 5.7 其它

| 方法 | body / 返回 |
|------|-------------|
| `POST /devices/{id}/interrupt` | 无；200 在 `turn_terminal` 之后 |
| `POST /devices/{id}/report` | `{ "playingMode": 1 }` 可选热更；202 `{ "sequence_number" }` |
| `POST /devices/{id}/stop` | 调 finalizer；200 时已 Stopped |
| `DELETE /devices/{id}` | 未 Stopped 则 finalizer，再删键；**不二次释放 permit** |
| `PUT /devices/{id}/config` | 非身份字段 200；改 device_id 400；Running 改企业/类型 409 |
| `POST /devices/{id}/faults` | `{ "fault": "skip_register" }`；禁改 id |
| `GET /devices/{id}/turns/{turn_id}` | 含终态字段 |
| `GET /devices/{id}/events?after_event_seq=` | 数组 |

`POST /templates`；`POST /scenarios/run`。

## 6–8. Scenario、silence、ACK

`wait: true` 等 `turn_terminal`。silence 为内部 PCM 全零。JSON ACK 字段见架构 §4.9。A/B/C 同一 device_type。

## 9. 故障注入

skip_register 新建后 Running+Connected 可 speak。skip_report：Running+Registered 可 speak。

## 10. 验收

### 10.1 交付

- [ ] 按 §5 的 body/状态码，Agent 能 speak、wait、订 WS、热更 playingMode
- [ ] 批量模板与错峰；多台对话
- [ ] UI 能靠 `turn_terminal` 刷新槽状态

### 10.2 正确性

- [ ] 并发重复 start：一路 202，其余 409，permit 不泄漏
- [ ] 超 max_connections 为 429 不是 409
- [ ] stop 后 delete、stop∥断线：permit 只 +1 再 −1
- [ ] `/wait`：Terminal 落在检查与注册之间时立即 200，不 504
- [ ] 所有 Turn 终态都有 `turn_terminal`，先 log 再唤醒
- [ ] register ACK∥timeout 只消费一次（单设备用例可在 P2 回归）
