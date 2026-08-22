# Phase 2 详细设计：多设备编排 + API + Scenario（v10）

架构完成矩阵、原子 report、register 超时、批量限额、ACK 字段见 `architecture.md`。  
并发：**默认 reject，可选 cancel_previous，不交付 queue**。

## 1. 目标

批量设备（模板、错峰、限额）、Control API、Scenario、PCM 时间轴、JSON ACK 与节流、Phase 3 查询。  
`wait` 锁内检查；Starting 必收口；断线后可再 start。

## 2. 范围与非目标

**范围：** Manager（模板、批量启停、stagger、`max_connections`、`max_concurrent_speaking`）；REST+WS；生命周期；锁内 wait；Scenario；silence；JSON ACK；A/B/C；查询/配置；reject/cancel_previous。

**非目标：** UI；queue；线上非 pcm；Mongo 预检；`sleep_ms:0` 清缓存；查 DownlinkAck；改 device_id；静默探针；上千连接压测。

## 3. Device Manager

```text
Created → Starting → Running → Stopping → Stopped → Deleted
```

`start`：Created/Stopped → 立即 202 → Starting。正常路径 Ready 后 Running。  
register 超时/拒绝、握手失败、读写错误、report 失败 → `fail_connection` → **Stopped**（先写 `connection_failed` 再唤醒 waiter）。Starting 不得永久停留。再次 start 允许。

**限额（启动时配置，可环境变量覆盖）：**

| 项 | 超限 |
|----|------|
| `max_connections` | 再 start / 批量 start 下一台 → **429** |
| `max_concurrent_speaking` | 再 speak → **409 或 429**（文档与实现选一，验收写死） |
| `per_device_buffer_bytes` | 丢最旧或反压，不得拖垮进程 |

单设备 panic/失败 recover，不影响其它实例。

## 4. Turn 并发

reject：槽非空 → 409。cancel_previous：§4.7 后 CAS。无 queue。  
验收：并发 speak；FinishingUpload；vad 取消保持 vad；**上行未 WaitingReply 时 command 不释放槽**。

## 5. API

路径 `{id}` = 创建时 device_id。

### 5.1 单设备生命周期

| API | 前置 | 原子动作 | 响应 |
|-----|------|----------|------|
| `POST /devices` | 无同 ID | 见 §5.2 | 201 |
| `POST /devices/{id}/start` | Created/Stopped | → Starting | 立即 202；超 max_connections → 429 |
| `POST /devices/{id}/speak` | Running；正常路径 Ready | CAS | 立即 202；槽满或 speaking 限额 → 409/429 |
| `POST /devices/{id}/speak_and_wait` | 同 speak | **同锁** CAS+waiter | 阻塞至 Terminal；超时用预算公式 |
| `POST /wait` | 必填 device_id | **同锁** 检查+注册 | 已满足立即 200；见 §5.3 |
| `POST /devices/{id}/interrupt` | 槽非空 | 取消表 | Terminal 后 200 |
| `POST /devices/{id}/report` | Ready | **report_mu 内**取号+递增+登记 pending | 立即 202 `{sequence_number}` |
| `POST /devices/{id}/stop` | 非 Deleted | §5.4 | Stopped 后 200 |
| `DELETE /devices/{id}` | 非 Deleted | 先 stop 再删键 | 200 |
| `PUT /devices/{id}/config` | 非 Deleted | 非身份字段 | 改 device_id → 400 |
| `POST /devices/{id}/faults` | start 前或 Stopped | 禁止改 ID | body 含 device_id → 400 |

### 5.2 批量、模板、错峰

**模板**

- `POST /templates` body：可复用的设备配置（无 device_id）。
- 或 `configs/templates/{template_id}.yaml`。
- `GET /templates` / `GET /templates/{template_id}`。

**创建**

```http
POST /devices
```

```yaml
# 单台
device_id: sim_001
# 或批量（互斥）
template_id: a3_pcm
count: 10
id_prefix: sim_batch
stagger_ms: 50          # 仅对后续 batch/start 有默认值；创建本身立即完成
```

批量生成 `id_prefix_1` … `id_prefix_{count}`（或 `{prefix}_{n}` 写死一种）。已存在 → **整批失败 409** 或不创建冲突项（选定：**整批失败**，避免半成功）。全部 Created 后 201，body 含 `device_ids[]`。

**批量启停**

```http
POST /devices/batch/start
POST /devices/batch/stop
POST /devices/batch/delete
```

```yaml
device_ids: [sim_batch_1, sim_batch_2]
stagger_ms: 50          # 省略则 default_stagger_ms
```

按数组顺序执行；相邻 start 的实际发起时间间隔 ≥ `stagger_ms`（可用单调时钟验收）。中途 429（连接上限）则已 start 的保持 Running，响应列出成功/失败 ID。delete 对每台先 stop。

### 5.3 wait

`turn_id` 或 `event_type` 至少一个。`after_event_seq` 排他游标。

锁内：Turn 已 Terminal → 立即 200；无 turn → 404；事件带游标先扫日志。省略游标只等未来。  
`GET /devices/{id}/events?after_event_seq=`。

### 5.4 stop 与 fail_connection

stop：Stopping → 取消表 → 清空 pending → 关 WS → Stopped。  
fail_connection：同序，**先 append `connection_failed` 再唤醒 waiter**。Stopped 可再 start。

### 5.5 查询与其它

`GET /devices`（含 state、last_error）；turns；frames；audio；config；events。  
`POST /scenarios/run`。`/ws/events`。  
skip_register：夹具 ID **新建**。

## 6. Scenario

`wait: true` 省略 timeout_sec 则用预算公式。纯指令断言 `command_received`，不要 `tts_done`。  
批量步骤可 `action: batch_start` + `stagger_ms`。

## 7. silence

内部 PCM 全零；禁止拼接 RIFF。Stage=4 先 vad。

## 8. ACK（本阶段增量）

字段表见架构 §4.9（JSON 指令含 ack、sequence_number、sleep_ms、code、topic、uuid）。

A/B/C：**同一 device_type** 且该类型 DownlinkAck=true；三台不同新建 ID；status=1；TTS ≥2 片。不是「同一 ACK mode」。

| 用例 | mode / sleep_ms | 期望 |
|------|-----------------|------|
| A | binary / 0 | 默认片间隔 |
| B | binary / 500 | 第二片起约 +500ms |
| C | json / 500 | 同 B；出站 `'1'` downlink-ack |

指令 JSON ACK 另用已注册设备，不与 B/C 共用。  
并发 report：keepalive + POST /report 序号唯一。

## 9. 故障注入

仅矩阵。batch skip_register：每个 ID 夹具新建。

## 10. 验收 Checklist

- [ ] `POST /devices` 批量：N 个不可变 ID；冲突整批 409
- [ ] 模板创建与单台配置等价可 start
- [ ] batch/start 错峰：相邻发起间隔 ≥ stagger_ms
- [ ] max_connections：第 N+1 台 start 429
- [ ] max_concurrent_speaking：超额 speak 409/429
- [ ] 可同时多台完成对话；单台崩溃不影响其它
- [ ] 并发 speak 409；vad 取消保持
- [ ] 上行未完 command 不释放槽
- [ ] wait 已 Terminal 立即 200；游标不丢事件
- [ ] register 无 ACK / nack → Stopped，可再 start；connection_failed 先于 waiter 可见
- [ ] report 并发取号唯一
- [ ] JSON ACK 字段完整；A/B/C 同一 device_type
- [ ] 预算公式；不可改 device_id
- [ ] 仅 IsFinal 可 silent
