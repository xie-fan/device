# Phase 2 详细设计：多设备编排 + API + Scenario（v9）

完成矩阵、取消表、`pending_reports`、`fail_connection`、ACK、身份见 `architecture.md`。  
并发：**默认 reject，可选 cancel_previous，不交付 queue**。  
Phase 1 已有 binary ACK 与完成矩阵；本阶段加 JSON ACK、非零 SleepMs、节流，以及 wait 锁与连接失败收口。

## 1. 目标

多设备、Control API、Scenario、PCM 时间轴、JSON/非零 SleepMs ACK、Phase 3 查询接口。  
`wait` 不得因事后注册而 504；异常断线后实例必须能再次 `start`。

## 2. 范围与非目标

**范围：** Manager；REST+WS；生命周期表；锁内 wait；`event_seq` 游标；Scenario；silence；`POST /report`；JSON ACK 与 A/B/C；查询/配置；reject / cancel_previous；`fail_connection`。

**非目标：** UI；queue；线上非 pcm；Mongo 预检；`sleep_ms:0` 清缓存；查 DownlinkAck；改 `device_id`；静默成功探针。

## 3. Device Manager

```text
Created → Starting → Running → Stopping → Stopped → Deleted
```

- `Created`：已有不可变 `device_id`，未连。
- `Starting`：`start` 已受理。
- `Running`：WS 打开。
- `Stopping`：清理中（stop 或 `fail_connection`）。
- `Stopped`：已清理；`last_error` 可空（主动 stop）或有值（失败）。**可再次 start。**
- `Deleted`：键已移除。

`start` 仅 Created 或 Stopped → 立即 202，进入 Starting。握手成功且（正常路径）Ready 后 → Running。  
**任何**握手/读写/远端关闭/report 发送失败 → §5.3 `fail_connection` → Stopped，**禁止**留在 Starting/Running。

## 4. Turn 并发

- `reject`：槽非空 → 409。
- `cancel_previous`：架构 §4.7，释放后再 CAS。
- 无 queue（501 或不暴露）。

验收：并发 speak；FinishingUpload 两种 Stage=2；已 `vad` 时取消保持 `vad`。

## 5. API 生命周期

路径 `{id}` = 创建时 `device_id`。

### 5.1 总表

| API | 前置 | 原子动作 | 成功响应时机 | 失败 |
|-----|------|----------|--------------|------|
| `POST /devices` | 无同 ID | 创建 Created | 立即 201 | 冲突 409 |
| `POST /devices/{id}/start` | Created 或 Stopped | → Starting；后台握手 | 立即 202；成功/失败走事件；失败见 §5.3 | Running/Starting 409；Deleted 404 |
| `POST /devices/{id}/speak` | Running；正常路径 Ready | CAS Reserved | 立即 202 `{turn_id, uplink_uuid}` | 槽占用/未 Ready 409 |
| `POST /devices/{id}/speak_and_wait` | 同 speak | **同一锁内** CAS + 注册 waiter | 阻塞至 Terminal 或预算超时 | CAS 失败立即 409；超时 504（Turn 继续） |
| `POST /wait` | 实例存在；必填 `device_id` | **同一锁内** 检查状态/日志 + 注册 waiter | 已满足则 **立即 200**；否则阻塞 | 见 §5.2 |
| `POST /devices/{id}/interrupt` | Running 且槽非空 | 取消表 | Terminal 后 200 | 槽空 409 |
| `POST /devices/{id}/report` | Running 且 Ready | 取号（首值=start）写入 pending | 立即 202 | 未 Ready 409 |
| `POST /devices/{id}/stop` | 非 Deleted | §5.3 主动 stop | Stopped 后 200 | Deleted 404 |
| `DELETE /devices/{id}` | 非 Deleted | 未 Stopped 则先 stop，再删键 | 200 | 已 Deleted 404 |
| `PUT /devices/{id}/config` | 非 Deleted | 非身份字段 | 立即 200 | 改 `device_id` → 400 |
| `POST /devices/{id}/faults` | start 前或 Stopped | 写 fault；禁止改 ID | 立即 200 | 冲突 409；body 含 `device_id` → 400 |

### 5.2 `speak` / `speak_and_wait` / `wait`

**`speak`：** CAS 成功立即 202。不注册 completion waiter。

**`speak_and_wait`：** 与 CAS **同一临界区**注册 waiter，无「返回后再注册」窗口。阻塞至 Terminal。`timeout_sec` 省略则用架构预算公式。504 / 客户端断开：只摘 waiter，Turn 继续。

**`POST /wait` 防丢失唤醒：**

请求体：

```yaml
device_id: required
turn_id: optional
event_type: optional          # 省略且带 turn_id = 等该 Turn Terminal
after_event_seq: optional     # 排他游标；见下
timeout_sec: optional         # 省略则用预算公式
```

至少 `turn_id` 或 `event_type` 之一。

在 **设备锁**（或等价版本号：读 `turn.generation` / 最新 `event_seq` 与注册 waiter 不可拆分）内：

1. **等 Turn 完成**（有 `turn_id`，无 `event_type` 或 `event_type=turn_terminal`）：
   - Turn 不存在 → **404**（不盲等未来 ID）
   - 已 Terminal → **立即 200**（快照：`turn_end_reason`、`reply_kind`、相关事件）
   - 仍活动 → 注册 waiter 后释放锁并阻塞
2. **按事件：**
   - 未给 `after_event_seq`：**只等未来事件**（锁内注册 waiter，不扫历史）。文档与 OpenAPI 写明。
   - 给定 `after_event_seq`：先扫 `event_seq > after_event_seq` 的日志；已有匹配 → **立即 200**；否则注册 waiter（仍持锁，避免扫完到注册之间漏事件）。

`GET /devices/{id}/events?after_event_seq=` 提供同一游标。日志至少保留 10000 条或本进程生命周期。

多个 waiter 在 Terminal / 匹配事件 / `fail_connection` 时一并唤醒。

### 5.3 `stop` 与 `fail_connection`

**主动 `stop`：**

1. → Stopping；并发 speak → 409。
2. 槽非空：取消表（套接字仍开则可发 Stage=3）。
3. 清空 pending；唤醒 waiter。
4. 关 WS；Disconnected；**Stopped**；`last_error` 清空。

**`fail_connection`：** 同序，但套接字已死则跳过出站；`turn_end_reason=connection_lost`；`last_error` 写入原因；事件 `connection_failed`。结束后 **Stopped，允许再 start**。

`delete`：先 stop/`fail` 收口，再删键。

### 5.4 其它端点

查询：`GET /devices`、`/{id}`（含 `state`、`last_error`、`event_seq`）、turns、frames、audio、config、events。  
Scenario：`POST /scenarios/run`、`GET /scenarios/runs/{run_id}`。  
faults：`skip_register` 用夹具 ID **新建**；错误时机 4xx。  
`/ws/events` 过滤 `device_id` / `turn_id` / `correlation_id`。

## 6. Scenario

```yaml
name: "basic_ptt_hello"
devices:
  - config: "configs/example_device.yaml"
steps:
  - action: start
  - action: speak
    audio: "testdata/hello.wav"
    wait: true
    # timeout_sec 省略 = 服务端按预算公式计算
  - assert:
      - type: event_received
        event: tts_done          # 仅预期 TTS 的用例
      - type: turn_end_reason
        reason: idle
      - type: asr_contains
        text: "你好"
        optional: true
```

纯指令用例 assert `command_received`，不要 assert `tts_done`。  
`wait: true` = `speak_and_wait`。skip_register：alloc → **create 新实例** → fault → start。

## 7. 流式发送与 silence

内部 PCM + 全零 silence；按 slice_ms 切 pcm；Stage=4 先记 vad 再补 Stage=2。禁止拼接 RIFF。

## 8. ACK 增量

运行时仍只看报文。本阶段：JSON ACK、`sleep_ms>0`、A/B/C。

`sleep_ms: 0` 不写缓存、不清旧值。A/B/C 三台 **新建** 不同 ID，同一 ACK 类型，`status=1`，TTS ≥2 片。

| 用例 | ACK | 期望 |
|------|-----|------|
| A | binary，0 | 默认片间隔 |
| B | binary，500 | 第二片起约 +500ms |
| C | json，500 | 同 B；出站 `'1'` downlink-ack |

指令 JSON ACK 另用已注册设备。乱序 report：keepalive + `POST /report` 按序号关联。

## 9. 故障注入

仅矩阵内 fault。`bad_seq` 不断言本地槽空。

## 10. 验收 Checklist

- [ ] 并发 speak 409；取消表与 vad 保持
- [ ] 无 queue
- [ ] `speak` 立即 202
- [ ] `speak_and_wait` 与 CAS 同锁注册 waiter
- [ ] Turn 已 Terminal 时 `wait` 立即 200，不 504
- [ ] 省略游标的事件 wait 只等未来；带 `after_event_seq` 能命中历史
- [ ] `wait`/`speak_and_wait` 默认超时 ≥ 预算公式，示例不得写死 30
- [ ] 纯 command / JSON 轮次按矩阵结束，不误 timeout
- [ ] 握手失败、杀 WS、report 发送失败 → Stopped + `connection_failed`，可再 start
- [ ] stop/delete 清理槽、计时器、pending、waiter
- [ ] 不可改 `device_id`；skip_register 新建实例
- [ ] JSON ACK 与 A/B/C；标志 0 不 ACK
- [ ] pending_reports 乱序；首个序号 = start
- [ ] Scenario 按预期 `reply_kind` 断言
