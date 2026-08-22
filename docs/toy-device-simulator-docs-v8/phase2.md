# Phase 2 详细设计：多设备编排 + API + Scenario（v8）

Turn 占用、取消表、fault 矩阵、`pending_reports`、下行计时、ACK 字段与身份规则见 `architecture.md`。  
并发策略：**默认 reject，可选 cancel_previous，不交付 queue**。  
Phase 1 已交付音频+指令 binary ACK；本阶段新增 JSON ACK、非零 SleepMs 与节流测试。

## 1. 目标

在 Phase 1 基础上支持：多设备批量、Control API（含生命周期契约）、高层原语、Scenario、连续 PCM 时间轴、JSON/非零 SleepMs ACK、Phase 3 所需查询接口。

## 2. 范围与非目标

**范围**

- Device Manager（生命周期、模板、错峰、资源限制、每设备 Turn 槽 CAS）
- REST + WebSocket 事件 API
- `speak` / `speak_and_wait` / `wait` / `stop` / `delete` 状态转移表
- Scenario YAML 执行器 + 报告
- 故障注入（矩阵 + 夹具新鲜 ID **新建实例**）
- 内部 PCM 时间轴 + silence
- playMode 热更新（`POST /report`，走 `pending_reports`）
- JSON ACK；非零 `sleep_ms`；音频节流 A/B/C
- Turn / 帧 / 音频查询、配置读写（`device_id` 不可变）
- 并发：reject（默认）与 cancel_previous

**非目标**

- Web UI、完整 live mic、上千连接压测
- queue（缺最大长度/取消/超时，推迟 Phase 4）
- 线上非 pcm
- Mongo 预检
- 用 `sleep_ms: 0` 清除节流缓存
- `dup_uuid` / `bad_stage` 的 drop 验收
- 查询服务端 `DownlinkAck` 配置
- 运行时改 `device_id` / 原子重键

## 3. Device Manager

实例状态：

```text
Created → Starting → Running → Stopping → Stopped → Deleted
```

- `Created`：已分配不可变 `device_id`，WS 未连。
- `Starting`：`start` 已受理，握手/注册/初始 report 进行中。
- `Running`：WS 仍打开。Connection 可能为 Connected / Registered / Ready（取决于 fault）。
- `Stopping`：正在取消 Turn、失败 pending report、关连接。
- `Stopped`：WS 已关，实例仍可 `GET` / 改非身份配置 / 再 `start`。
- `Deleted`：键已从 Manager 移除。

其它职责：模板批量生成 **互不冲突** 的 `device_id`；批量 stagger；`max_connections` / `max_concurrent_speaking`；单设备 recover；事件按 `device_id` / `turn_id` / `correlation_id` 过滤。

## 4. Turn 并发

- 默认 `reject`：槽非空 → **409**，不发帧。
- 可选 `cancel_previous`：按 `architecture.md` §4.7 全表转移（含 Reserved、FinishingUpload、VAD 已写入时保持 `vad`），释放槽后再 CAS 新 Reserved。
- 不交付 `queue`。未实现的 queue 接口应 501 或根本不暴露。

`interrupt` API 遵守同一张取消表。

验收必须覆盖：

- 两个并发 HTTP `speak`：一个 Reserved，一个 409
- FinishingUpload：Stage=2 已发 / 未发
- 已记 `vad` 时取消：`uplink_end_reason` 仍为 `vad`

## 5. API 生命周期契约

路径中的 `{id}` **永远**是创建时的 `device_id`。

### 5.1 总表

| API | 前置 | 原子动作 | 成功响应时机 | 失败 |
|-----|------|----------|--------------|------|
| `POST /devices` | 无同 ID 实例 | 创建 Created；写入不可变 `device_id` | **立即** 201，body 含 `device_id` | ID 冲突 409 |
| `POST /devices/{id}/start` | Created 或 Stopped | 进入 Starting；后台握手 | **立即** 202；Ready/失败走事件 | 已 Running 409；Deleted 404 |
| `POST /devices/{id}/speak` | Running；正常路径还需 Connection==Ready | CAS Reserved；后台发帧 | **立即** 202 `{turn_id, uplink_uuid}` | 槽占用 409；未 Ready 409 |
| `POST /devices/{id}/speak_and_wait` | 同 speak | 同 speak，并注册 completion waiter | **阻塞**至 Terminal 或 `timeout_sec` | CAS 失败立即 409；超时 504（Turn **继续**） |
| `POST /wait` | 实例存在；body **必填** `device_id` | 注册事件 waiter，不创建 Turn | **阻塞**至匹配事件/Terminal 或超时 | 缺 `device_id` 400；超时 504 |
| `POST /devices/{id}/interrupt` | Running 且槽非空 | 执行取消表 | Turn Terminal 后 200（短超时内）；槽已空 409 | Deleted 404 |
| `POST /devices/{id}/report` | Running 且 Ready | 取号写入 `pending_reports`（kind=manual）后发送 | **立即** 202 `{sequence_number, correlation_id}`；回显走事件 | 未 Ready 409 |
| `POST /devices/{id}/stop` | 非 Deleted | 见 §5.3 | stop 完成（Stopped）后 200 | 已 Deleted 404 |
| `DELETE /devices/{id}` | 非 Deleted | 若未 Stopped 先执行 stop，再移除键 | 删除完成后 200 | 已 Deleted 404 |
| `PUT /devices/{id}/config` | 非 Deleted | 更新非身份字段 | 立即 200 | 试图改 `device_id` → 400 |
| `POST /devices/{id}/faults` | Starting 之前，或 Stopped 后再 start | 写入 fault；**禁止**改 ID | 立即 200 | 已走过冲突步骤 409；body 含 `device_id` → 400 |

注入 speak：`skip_register` 允许 Connected；`skip_report` 允许 Registered。其余同 CAS 规则。

### 5.2 `speak` vs `speak_and_wait` vs `wait`

**`speak`**

- HTTP 在 CAS 成功后 **立即** 返回。`turn_id` 在 Reserved 时已分配，出现在 202 body 中。
- 不注册 completion waiter。调用方若要等结束，另调 `wait`。

**`speak_and_wait`**

- 不是「受理后立刻结束 HTTP」。
- CAS 失败：立即 409，无 Turn。
- CAS 成功：HTTP **保持打开**，直到：
  - Turn **Terminal**（idle / timeout / interrupt / error），或
  - 请求 `timeout_sec`（默认 30，且 ≥ `first_reply_timeout_sec`）。
- 200 body：`turn_id`、`uplink_uuid`（Reserved 时值）、`turn_end_reason`、`uplink_end_reason`。
- HTTP 超时 504：注销本请求 waiter；**Turn 继续**；槽不释放。
- **调用方断开**（客户端 abort）：与 504 相同——只注销 waiter；Turn 继续；不发 Stage=3。需要打断请显式 `interrupt` 或 `stop`。

**`POST /wait`**

- 必填 `device_id`；可选 `turn_id`、`event_type`、`timeout_sec`（默认 30）。
- 至少指定 `turn_id` 或 `event_type` 之一。
- 不创建、不占用 Turn。
- 断开 / 超时：只取消本 waiter。

多个 waiter（若干 `wait` + 一个 `speak_and_wait`）在 Terminal 时一并唤醒。

### 5.3 `stop` / `delete` 与活动 Turn

**`stop` 顺序（不可颠倒）：**

1. 实例 → Stopping（并发 `speak` 得 409）。
2. 若槽非空：执行取消表（Reserved 不发 Stage=3；其余按表可能发 Stage=3）；取消首包/idle 计时器；Turn Terminal；释放槽。
3. 所有 `pending_reports` 项失败：`report_timeout` 或明确 `cancelled`；清空表。
4. 唤醒该设备上全部 `speak_and_wait` / `wait`（Turn 已 interrupt 或 wait 被取消）。
5. 关闭 WebSocket；Connection → Disconnected。
6. 实例 → Stopped。然后 200。

**`delete`：** 若不是 Stopped，先完整执行 `stop`，再从 Manager 删除键。之后任何 `{id}` → 404。

### 5.4 其它端点

**查询（Phase 3 依赖，必须交付）**

- `GET /devices` / `GET /devices/{id}`
- `GET /devices/{id}/turns`
- `GET /devices/{id}/turns/{turn_id}`
- `GET /devices/{id}/turns/{turn_id}/frames`
- `GET /devices/{id}/turns/{turn_id}/audio/uplink`
- `GET /devices/{id}/turns/{turn_id}/audio/downlink`
- `GET /devices/{id}/config`

**Scenario**

- `POST /scenarios/run`
- `GET /scenarios/runs/{run_id}`

**故障注入**

- `POST /devices/{id}/faults`
- 必须在对应生命周期步骤之前，或先 stop 再 start
- `skip_register`：**禁止**改现有实例 ID。流程：`alloc_fresh_id` → `POST /devices`（夹具 ID）→ `POST .../faults` → `start` → `speak`
- 已走过 register 再设 `skip_register` → 400/409
- 已 Ready 再设 `skip_report` → 400/409
- 不要在 handler 里写死「必定 drop」

**事件**

- WebSocket `/ws/events` — 过滤含 `device_id`、`turn_id`、`correlation_id`

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
    timeout_sec: 30
  - assert:
      - type: event_received
        event: tts_done
      - type: turn_end_reason
        reason: idle
      - type: asr_contains
        text: "你好"
        optional: true
```

`tts_done`：至少一帧匹配 UUID 的 TTS，且 DownlinkPlayer 因 idle 回到 Idle。零下行不是 `tts_done`。

`skip_register`：`alloc_fresh_id` → **create 新实例**（该 ID）→ fault → start。验收读 `fresh_ids.jsonl`。  
`skip_report` 不得 assert `expected_server_drop`。

Scenario 的 `wait: true` 语义与 `speak_and_wait` 相同（等 Terminal，断开不取消 Turn）。

## 7. 流式发送与 silence

```yaml
stream:
  - type: audio
    file: "speech_seg1.wav"
  - type: silence
    duration_ms: 1500
  - type: audio
    file: "speech_seg2.wav"
  - type: silence
    duration_ms: 3000
```

1. 每段 WAV：去掉 RIFF 容器，得到 PCM。
2. 转到内部 PCM：mono、s16le、`sample_rate`。多声道则 downmix 或拒绝。
3. `silence`：在内部 PCM 上生成 `duration_ms` 全零采样。
4. 整条时间轴拼接后按 `slice_ms` 切 **pcm 字节** 成帧发出（`format=pcm`）。
5. 收到 Stage=4：立即记 `vad`（若空），停止后续段，补 Stage=2。

禁止拼接带 RIFF 头的文件字节。上行 `frames.jsonl` 音频 payload 不得出现第二个 RIFF。

## 8. ACK 契约（本阶段增量）

运行时触发仍只看报文，见 `architecture.md` §4.9。

### 8.1 配置

```yaml
behavior:
  downlink_ack:
    mode: binary          # binary | json
    sleep_ms: 0           # 写入 ACK 的 SleepMs
    code: 0
```

**`sleep_ms: 0` 语义（对齐基线 `UpdateMemoryState`）：**

- SleepMs 与 MemoryPercent 均为 0 时，服务端 **跳过写入** 节流缓存。
- **不能**清除该 device 上已有的正值 SleepMs。
- 「不节流」仅当该设备缓存中本就没有正值状态时成立（新设备，或 TTL 过期，默认约 60s）。

因此 0 / binary+正值 / JSON+正值必须使用 **不同 device_id**（三台 **新建** 实例，不可改 ID）。禁止同一设备上「先 500 再 0 / 换 mode」当作独立对照。

### 8.2 本阶段验收范围

- JSON 音频 ACK、JSON 指令 ACK（字段见架构 §4.9）。
- binary 指令 ACK 已在 Phase 1 交付；本阶段回归即可。
- 非零 `sleep_ms` 写入出站 ACK。
- 节流 A/B/C。

### 8.3 节流验收 A/B/C

仅测音频 TTS。三台设备 **全部** 满足：

1. 已走正常路径 register 成功，业务上 `status=1`（不是 skip_register）。
2. **同一** `device_type`，且该类型在 core fixture 中 `DownlinkAck=true`（保证 TTS 头 `NeedAck==1`）。
3. 同一条足够长的 pcm，下行 TTS **至少 2 个分片**。
4. 三个 **不同** `device_id`，均为创建时指定，测试中不改键。

服务端另有约 300ms 片间隔。`speedCtrl` 默认开；SleepMs 封顶默认 6000ms。

| 用例 | ACK | 期望 |
|------|-----|------|
| A | binary，sleep_ms=0 | 片间隔 ≈ 服务端默认；0 ACK 不写入节流缓存 |
| B | binary，sleep_ms=500 | **从第二片起**相对 A 增大约 500ms（不超过 500ms+封顶+片间隔） |
| C | json，sleep_ms=500 | 同 B；出站为 `'1'` + `downlink-ack` |

`frames.jsonl` 核对出站 ACK 内容。

若某台收到 `NeedAck==0`：停止节流断言，报 **fixture**，不算模拟器实现失败。

指令 JSON ACK：可用 sleep_ms=0；**另用**已注册设备，不与 B/C 共用。

乱序 report：Ready 之后短间隔 keepalive + `POST /report`，断言两条 `report_echo` 按序号关联，即使到达顺序与发送顺序不同。

## 9. 故障注入

仅矩阵内 fault。批量 `skip_register`：每个 ID 都来自夹具且对应 **新建** 实例，验收读 jsonl。  
`dup_uuid` / `bad_stage`：可注入并录帧，不进入 drop 的 pass/fail。  
`bad_seq` 不断言本地槽空。

## 10. 验收 Checklist

- [ ] 可同时启动可配置数量的设备并完成对话；批量错峰
- [ ] 并发 speak：CAS 一个 Reserved、一个 409（reject）
- [ ] cancel_previous 覆盖 Reserved / Speaking / FinishingUpload（Stage=2 已发与未发）/ WaitingReply
- [ ] 已记 `vad` 时取消：`uplink_end_reason` 仍为 `vad`；已 Stage=2 且无 VAD：保持 `stage2`；`turn_end_reason=interrupt`
- [ ] 无 queue API，或明确 501
- [ ] `speak` 立即 202 且含 Reserved 时的 `turn_id`
- [ ] `speak_and_wait` HTTP 阻塞至 Terminal；504 / 客户端断开后 Turn 仍继续
- [ ] `POST /wait` 无 `device_id` 时拒绝；断开只取消 waiter
- [ ] `stop` 对活动 Turn 走取消表、释放槽、唤醒 waiter、再关 WS
- [ ] `delete` 先 stop 再删键；之后 404
- [ ] `PUT /config` 或 faults 改 `device_id` → 400
- [ ] skip_register 用夹具 ID **新建**实例，不重键
- [ ] faults 在错误时机返回 4xx
- [ ] Phase 3 所需查询/配置/音频下载 API 全部可用
- [ ] 事件：连接级用 `correlation_id`（含 Turn 中的 report）；对话用 `turn_id`
- [ ] Scenario：正常路径 `tts_done`+idle；注入按矩阵；skip_register 核对 jsonl
- [ ] silence：内部 PCM 全零分帧；无重复 RIFF
- [ ] Stage=4 先记 vad 再补 Stage=2
- [ ] 标志为 0 时无出站 ACK
- [ ] 模拟器代码路径无 DownlinkAck 配置查询
- [ ] JSON 音频/指令 ACK 字段符合架构 §4.9
- [ ] A/B/C 满足 §8.3 四条前置；≥2 片 TTS；三台不同 ID
- [ ] `/report` 与 keepalive 共用 `pending_reports`；乱序回显不串 waiter
- [ ] playingMode 可通过 report 热更新
- [ ] 单设备异常不影响其他设备
