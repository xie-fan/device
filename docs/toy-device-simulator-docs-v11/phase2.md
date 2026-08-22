# Phase 2 详细设计：多设备编排 + API + Scenario（v11）

本阶段交付：**批量并发 + Agent API**。playingMode 可 report 热更新。  
并发默认 reject，可选 cancel_previous，不交付 queue。  
架构细则见 `architecture.md`；下文含本阶段可实施契约。

## 1. 目标

模板批量、错峰、原子限额、REST+WS、Scenario、PCM 时间轴、JSON ACK、查询接口。  
wait 锁内检查；fault 实例能 Running；断线后可再 start。

## 2. 范围与非目标

**范围：** Manager；conn/speak permit；批量 API；生命周期；锁内 wait；Scenario；silence；`POST /report` **热更新 playingMode**；JSON ACK 与 A/B/C；查询/配置。

**非目标：** UI；queue；线上非 pcm；Mongo 预检；`sleep_ms:0` 清缓存；查 DownlinkAck；改 device_id；上千连接压测。

## 3. Device Manager 与 Running 映射

```text
Created → Starting → Running → Stopping → Stopped → Deleted
```

| 路径 | Starting→Running 当 | speak 要求 |
|------|---------------------|------------|
| 正常 | Connection=Ready | Running 且 Ready |
| skip_register | Connection=Connected | Running 且 Connected |
| skip_report | Connection=Registered | Running 且 Registered |

GET 返回 `instance_state` + `connection_state` + `last_activity` + `playing_mode`。

**原子闸门**

- `conn_permit`（`max_connections`）：进入 Starting 前 TryAcquire；失败 429。Stopped / fail_connection / Deleted **恰好释放一次**。
- `speak_permit`（`max_concurrent_speaking`）：CAS Reserved **成功路径内** TryAcquire；CAS 失败回滚 permit。Turn Terminal 恰好释放一次。
- 禁止无锁读计数。验收：`max_connections+k` 路并发 start，成功数 = max；超额 speak 全部 409/429。

start 仅 Created 或 Stopped。Starting 再 start → 409。

fail_connection：**先 Stopped 并释放 conn_permit**，再写 `connection_failed`，再唤醒 waiter。随后立即 start 不得 409。

## 4. Turn 并发

reject：槽非空 → 409。cancel_previous：取消表后 CAS。无 queue。

## 5. API

`{id}` = 创建时 device_id。

### 5.1 单台

| API | 要点 |
|-----|------|
| `POST /devices` | 单台或批量；见 §5.2 |
| `POST /devices/{id}/start` | TryAcquire conn_permit；202 |
| `POST /devices/{id}/speak` | Running + Connection 前置（§3）；CAS+speak_permit |
| `POST /devices/{id}/speak_and_wait` | 同锁 CAS+waiter；超时用预算公式 |
| `POST /wait` | 同锁检查；已 Terminal 立即 200；游标过期 410 |
| `POST /devices/{id}/interrupt` | 取消表 |
| `POST /devices/{id}/report` | Ready；锁内取号；body 可含 `playingMode` **热更新** |
| `PUT /devices/{id}/config` | 改 device_id → 400；改 enterprise/device_type 且 Running → 409（须重连） |
| `POST /devices/{id}/stop` / `DELETE` | 释放 permit |
| `POST /devices/{id}/faults` | 禁止改 ID；start 前或 Stopped |

### 5.2 批量 / 模板 / 错峰

- `POST /templates`；`configs/templates/{id}.yaml`
- `POST /devices`：`template_id` + `count` + `id_prefix` → `{prefix}_{n}`；冲突 **整批 409**
- `POST /devices/batch/start|stop|delete`：`device_ids` + `stagger_ms`（默认 50）；相邻 start 间隔 ≥ stagger；中途 429 列出成功/失败

### 5.3 wait 与 event_log

`turn_id` 或 `event_type` 至少一个。`after_event_seq` 排他。省略游标只等未来。日志 10000 条或 24h；游标小于 oldest → 410 `event_seq_expired`。

### 5.4 查询

`GET /devices`、turns、frames、audio、config、events。`POST /scenarios/run`。`/ws/events`。

## 6. Scenario

`wait: true` 省略 timeout 则用预算。纯指令不要 assert `tts_done`。可 `batch_start` + stagger。

## 7. silence

内部 PCM 全零；禁止拼接 RIFF。

## 8. ACK 与 report

JSON 字段见架构 §4.9。A/B/C 同一 **device_type**（DownlinkAck=true），不是同一 ACK mode。

`POST /report` 与 keepalive 共用取号；payload 含当前 `playingMode`。热更后后续 report 带新值。验收：热更 playingMode 后服务端行为随新模式（至少本地记录与出站字段一致）。

## 9. 故障注入

矩阵内 fault。skip_register 夹具 **新建** 后 start → Running（Connected）。skip_report → Running（Registered）后可 speak。

## 10. 验收

### 10.1 交付

- [ ] 批量创建 N 台、模板、错峰 start、多台完成对话
- [ ] playingMode 经 `POST /report` 热更新；身份字段 Running 时不可改
- [ ] Agent：speak / wait / 查询 / 事件 WS 可用
- [ ] 单台崩溃不影响其它

### 10.2 正确性

- [ ] 并发 start 不超过 max_connections；permit 释放后可再 start
- [ ] 并发 speak 不超过 max_concurrent_speaking；Terminal 后释放
- [ ] skip_register / skip_report 实例为 Running，speak 前置符合 §3
- [ ] wait 已 Terminal 立即 200；过期游标 410；Stopped 后再 connection_failed
- [ ] 全部出站 writePump；report 并发序号唯一
- [ ] JSON ACK 字段完整；A/B/C 同一 device_type
- [ ] 不可改 device_id
