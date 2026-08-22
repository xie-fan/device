# Phase 1 实现配套清单

本文是实现清单，**不修改**正式契约。冲突时以同目录 `architecture.md`、`phase1.md` 与 `docs/toy-device-websocket-protocol.md` 为准。

标记：

| 标记 | 含义 |
|------|------|
| 契约 | 正式文档已写死，必须按此做 |
| 推断 | 正式文档只有路径/名称，没有字段级 schema；此处给出一份可落地默认，两个人应对齐这份，而不是各写一套 |
| stub | 类型/函数要在，行为为空或恒成功，给 Phase 2 留接口 |
| 禁止 | 本阶段不得出现（HTTP 路由、UI、第二套队列等） |

---

## 0. 交付一句话

one-shot CLI，主路径 `playing_mode=1`：

**Dial（Device 三段 + Action=chatbot）→ register ACK code=0 → 初始 report 回显 → Ready → 先读 WAV 再 CAS → pcm Stage=1…→2 → 完成矩阵内某条合法终态 → `--wait` 结束 → 落盘 → BeginClose 三段收口 → Disconnected。**

不做：REST、批量、UI、Scenario、JSON ACK、非零 `SleepMs`、speak backlog、改 `device_id`、HTTP `/interrupt`、事件 WS、tombstone、asset、conn_permit 限额。

---

## 1. 开工前置（写业务代码之前）

基线仓库：`C:\Users\xie_f\projects\other\ai-creates-wealth`  
基线提交：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
Gorilla：`github.com/gorilla/websocket v1.5.3`  
语言：Go（契约：按字节收 `TextMessage` 中的二进制，禁止 UTF-8 解码下行音频）

| # | 动作 | 标记 |
|---|------|------|
| 1 | `git checkout` 到该提交的干净树，不要混未提交改动 | 契约 |
| 2 | 不要用 `projects/go/ai-creates-wealth` | 契约 |
| 3 | 对照 `common/types/newProtocol.go` 的 `AudioHeader`：`encoding/binary.Size == 100`（含 2 字节 padding） | 契约 |
| 4 | 从该结构 **encode 已知字段** 生成 `testdata/golden_frames/`，禁止把 `example/asr/mock.go` 发出的帧收进 golden | 契约 |
| 5 | 对该提交的 core 做一次 PCM/TTS 冒烟：真实 WS 至少收到一帧 `'0'` TTS 且未因 UTF-8 失败 | 契约 |
| 6 | 确认本地 core：`ws://127.0.0.1:8089/`、企业配置、设备类型（**不要用 MH 前缀**，否则 Seq 用例会假通过）、Mongo 设备 `status=1` | 契约 + 协议 |

mock.go **不要复制**：管理首字节 `0x01`、不发 report、主路径不发 Stage=2、UUID 写死 1。

---

## 2. 建议目录（契约只给了包名，文件名是推断）

```text
toy-device-simulator/
├── go.mod
├── protocol/
│   ├── header.go            # AudioHeader 100B LE
│   ├── header_test.go       # vs golden_frames
│   ├── envelope.go          # '1' + {topic,data}
│   ├── ack.go               # '4' + 28B DeviceAckMsg
│   └── jsonmsg.go           # 无前缀 { Code / Action }
├── core/
│   ├── device.go            # device_mu、Connection 状态、槽
│   ├── conn.go              # Dial / 读循环 / writePump
│   ├── outbound.go          # 单一队列、Enqueue、CancelTurn、BeginClose
│   ├── turn.go              # CAS Reserved、terminalLocked
│   ├── events.go            # appendEventLocked、内存 event_log
│   ├── register.go
│   ├── report.go            # pending_reports、keepalive
│   ├── downlink.go          # 完成矩阵、early_downlink_buf、计时器
│   ├── finalize.go          # request_finalize + Phase A/B/C
│   └── audio.go             # WAV → 内部 PCM → slice_ms 切片
├── config/
│   └── config.go            # YAML 加载与启动拒绝
├── recording/
│   └── recorder.go          # 异步落盘，不进读循环
├── cmd/speak/main.go
├── cmd/check/main.go
├── cmd/fixture/main.go
├── configs/example_device.yaml
└── testdata/
    ├── golden_frames/
    ├── fixtures/fresh_ids.jsonl
    ├── inbound/             # 本阶段可空；给 ACK 单测留位
    └── audio/hello.wav      # 16kHz mono s16le PCM 的 WAV
```

`configs/manager.yaml`、`configs/templates/`、`data/assets/`：**禁止**本阶段创建并当功能用。

---

## 3. architecture.md 裁剪（本阶段读哪些）

### 3.1 必实现（设备连 core 的那条 WS）

| 段落 | 本阶段要落地的部分 |
|------|-------------------|
| §2 原则、§5 硬约束（设备侧） | 先拷贝再 CAS；Stage=4 先 vad；打断用 CancelTurn；关连接只用 BeginClose；Terminal 只经 `terminalLocked` |
| §4.1 Turn / `terminalLocked` / `appendEventLocked` | 函数契约全文。Phase 1：`eventWaiters`/`slowSubs`/hub 恒空，**仍必须走这两个函数写日志** |
| §4.1 外层模板、Phase A/C 模板 | 失败 JSON / 完成矩阵走外层模板；进程退出走 Phase A → B → C |
| §4.4 完成矩阵 | 与 `phase1.md` §6 同一张表 |
| §4.6 注入矩阵 | CLI `--inject` |
| §4.7 取消表、`CancelTurn`、`BeginClose` | 全文，含 `CancelResult`/`CloseResult` |
| §4.8 report | `report_mu` 内取号，**解锁后再 Enqueue** |
| §4.9 outbound buffer | 单一物理队列；`depth>=2`；数据 `len>=depth-1` 拒；Stage=3 `len>=depth` 拒；BeginClose 的 `len` 取 keep |
| §4.10 ACK | **仅** binary、`SleepMs=0`；音频 DownlinkType=1/2；指令=3 |
| §4.12 锁与 finalizer | 无 `manager_mu`。锁顺序 `device_mu → conn_mu → report_mu`（Cancel/BeginClose 再取 `writePump_mu`）。泵不回锁。closer **不得**是读循环自己 |
| §4.13 keepalive | Ready 后周期 report，默认 60s，防 360s 踢线。`skip_report` 不发 keepalive、不停 Ready |
| §4.14 读循环 | 持续读；落盘异步；失败 JSON **不得**自己关 socket |
| §6 音频管线 | WAV 解 RIFF → 内部 PCM → 按 `slice_ms` 切 **pcm 字节**；禁止逐片封装 WAV |
| §10 基线 | 见 §1 |

### 3.2 stub（类型留下，行为空）

| 概念 | Phase 1 行为 |
|------|----------------|
| `instance_id` | 进程启动分配一次（推断：`ins_` + UUID）。写入每条事件。录音目录 **不** 含它 |
| `conn_generation` | 本进程 Dial 成功后置 1，退出不再复用连接 |
| `speak_permit` / `conn_permit` | 不计数、不 429；`terminalLocked` 里「释放 permit」做成 once no-op |
| completion waiter | 只服务 CLI `--wait`。无 HTTP |
| event waiter 表 / wsHub / `close_mode` | 空表、不建 hub。`appendEventLocked` 仍按契约走步骤 1（append 日志），步骤 2–3 空操作 |
| `speakable_waiters` | 空表。Phase C 摘表是空循环 |
| tombstone / `device_deleted` | 不实现。进程退出即结束 |

### 3.3 禁止实现

| 段落 | 原因 |
|------|------|
| §4.1 wsHub 全文、事件 WS Ping/Pong、`requestCloseLocked` 那套 | 那是 **Agent 事件 WS**，不是设备连 core 的 WS。README「实现契约」里 live WS 条款 **本阶段不套到设备泵** |
| §4.2 HTTP 游标 / 410 / tombstone 路由 | 无 REST |
| §4.3 `/wait`、`wait_ready` | 无 REST；`--wait` 只等 completion |
| §4.5 Scenario | Phase 2 |
| §4.11 PUT allowlist | 无 HTTP |
| §4.15 限额 409/429 | 无 Manager |
| §4.16 `POST /assets`、`stream`、HTTP 下载 | CLI `--audio` 即可 |
| JSON ACK、非零 SleepMs、A/B/C | 配置出现则 **启动拒绝** |
| speak backlog | Phase 4 |
| MQTT、`'2'` 拍照、`'3'` 转发 | 协议范围外 |

设备泵自己的写出时限（推断，避免 Write 挂死）：每次 `WriteMessage` 前 `SetWriteDeadline(now + write_drain_timeout_sec)`。这不是事件 WS 的空闲 Ping。设备保活用 **周期 report**，不要对 core 连接乱发 WebSocket Ping。

---

## 4. 建议实现顺序（工作包）

后包依赖前包。每个包有可独立验证的出口。

### WP0 基线与 golden

- 检出基线；确认 `AudioHeader` 100 字节
- `testdata/golden_frames/` 至少：空 payload 的 Stage=1 Seq=0 一帧；Stage=2 空载荷一帧
- 出口：`go test ./protocol` 与 golden 逐字节一致

### WP1 protocol

识别首字节 `'0'` `'1'` `'4'` `'{'`。  
`AudioHeader` 字段见协议 §6。`DeviceAckMsg` 28 字节 LE 见协议 §10.1。  
管理信封：`'1'` + `{"topic","data"}`，topic 恰好 5 段 `{ent}/{type}/{id}/{scene}/{server|client}`。  
出口：单测 encode/decode；坏头（不足 100 字节）decode 失败且 **不占 Turn 槽**。

### WP2 config + `cmd/check`

加载 `phase1.md` §7 YAML。启动拒绝：

| 条件 | 行为 |
|------|------|
| `audio.format` ≠ `pcm` | 非 0 退出 |
| `playing_mode` 不是 1/2/3 | 非 0 退出 |
| `downlink_ack.mode` = json | 非 0 退出 |
| `downlink_ack.sleep_ms` ≠ 0 | 非 0 退出 |
| `write_queue_depth` < 2 | 非 0 退出 |
| `write_drain_timeout_sec` ≤ 0 | 非 0 退出 |
| 缺必填身份字段 / `action` ≠ `chatbot` / `server.url` 空 | 非 0 退出（推断） |
| `uuid.min` < 1 或 `uuid.max` > 2147483647 或 min>max | 非 0 退出（推断） |

合法配置：`cmd/check` 退出 0。本阶段 **不要** 去加载 `manager.yaml`。

### WP3 outbound buffer + writePump

单一 `[]frame` 队列。`len` = 已入队尚未取出 Write 的帧数（正在 Write 的那一帧不计）。

| 种类 | 入队 | 拒绝 |
|------|------|------|
| 数据（Stage=1/2、ACK、report、keepalive） | `len < depth-1` | `len >= depth-1` → 数据满，走 `request_finalize_async(write_backpressure)` |
| Stage=3 | `len < depth` | `len >= depth` → `backpressure` |
| 同 `uplink_uuid` 已有未写出 Stage=3 | 不追加，返回 `enqueued` | — |

`CancelTurn` / `BeginClose` 按 architecture §4.7 原文做，含过滤算法。泵协程 **永不** 取 `device_mu`/`conn_mu`。

出口：单测队列边界（depth=2：1 格数据 + 1 格 Stage=3）；BeginClose 保留已入队 Stage=3。

### WP4 连接与读循环

状态：`Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready`。  
收口：`Disconnecting → BeginClose → closer drain/close → Disconnected`。

Dial（契约 + 协议）：

- URL：`device.server.url`
- Header：`Device: {enterprise}/{device_type}/{device_id}`（恰好 3 段）、`Action: chatbot`
- 用 Gorilla；读侧按 `[]byte` 处理 `TextMessage`

读循环：按首字节分流；解码失败记 `protocol_error`，不占槽；**禁止**同步写盘；**禁止**因失败 JSON 关 socket。

出口：能连上 core，读到任意合法帧不崩溃。

### WP5 register / report / keepalive

**register（契约）**

1. 持 `conn_mu`：状态 `Registering`，分配 `register_attempt_id`，settle=pending，启动 `register_ack_timeout_sec` timer
2. **解锁后** Enqueue `'1'` + register JSON
3. ACK 与 timeout 对同一 attempt 做 `try_consume`，**只能成功一次**
4. `Timer.Stop` **不能**代替消费（callback 可能已在跑）
5. 失败：解锁后 `request_finalize_async`，不要持锁关 socket
6. `--inject skip_register`：不发送、不装 timer，停在 **Connected**

register `data`（协议 §4.1 + YAML）：

```json
{
  "sequence_number": 0,
  "enterprise": "...",
  "device_type": "...",
  "device_id": "...",
  "firmware_version": "...",
  "nic_type": "...",
  "nic_iccid": "..."
}
```

成功：topic 以 `/register/client` 结尾且 `data.code==0` → `Registered`，事件 `registered`。  
`code != 0`：事件 `ack_failure`，async 收口。

**report（契约）**

- `report_next_seq` 初值 = `report_sequence_start`（默认 1），**第一个序号等于 start**
- 持 `report_mu`：取号、递增、登记 `pending_reports[seq]`，**解锁后再 Enqueue**
- 仅 `kind=initial` 且当前 `Reporting` → 收齐回显后 `Ready`
- `--inject skip_report`：不发送，停在 **Registered**（此时允许 speak）
- keepalive：Ready 后每 `keepalive_interval_sec` 再发一条 **同计数器** 的 report；`skip_report` 不发
- 回显命中 pending → `report_echo`；未命中 → `report_echo_unmatched`；超时 → `report_timeout`

report `data`（协议 §5.1）。YAML 未给电量/信号时的推断默认：`code=0`，`signalStrength=0`，`batPowerLevel=100`，`playingMode` 用配置。

speakable（CLI 版，契约表的子集）：

| 路径 | 可 speak |
|------|----------|
| 正常 | Connection=`Ready` |
| skip_register | `Connected` |
| skip_report | `Registered` |

否则拒绝本次 speak（CLI 非 0），**不得**留下 Reserved 槽。

### WP6 音频、Turn、完成矩阵

顺序（契约：禁止先占槽再读文件）：

1. 读完 `--audio` WAV 到内存，校验 RIFF PCM，且 `sample_rate/channels/sample_format` 等于配置，否则启动后、CAS 前失败（不占槽）
2. 按 `slice_ms` 切 pcm 字节（16kHz s16le 100ms = 3200 B）。每片 ≤ `max_payload_size`（51200）
3. 短持 `device_mu`：检查 speakable → CAS 槽 `Reserved` → 登记 uuid（范围 `uuid.min..max`）、`seq_before`、挂 PCM
4. 解锁后由上行协程经 outbound 发送：`Seq=0` 的 Stage=1 起，最后 Stage=2（payload 可空）

Turn 状态：`Reserved | Speaking | FinishingUpload | WaitingReply | Terminal`。

硬规则：

- 新一轮必须 Seq=0 + Stage=1（设备类型不要用 `MH` 前缀）
- Stage=4：若 `uplink_end_reason` 仍空则先写 `vad`，停 Stage=1，补 Stage=2；非空则不得覆盖
- `uplink_end_reason` 先写不改
- **WaitingReply 之前**：不启动完成计时、不 Terminal、不释槽（失败 JSON 除外）。提前下行进 `early_downlink_buf`（容量 32，溢出丢最旧并 `early_downlink_overflow`）
- 进入 WaitingReply 后 **回放** 缓冲：只驱动计时器，禁止二次 ACK / 事件 / 录帧 / 落盘
- 下行 TTS **不要**等 Stage=2；用 idle

关联判定见 `phase1.md` §6。终止全部经 `terminalLocked` 写 `turn_terminal`。

`--wait` 预算（契约公式）：

```text
upload + first_reply + max(idle, followup, post_final_asr_silence) + slack
```

`upload` 推断：`ceil(pcm_bytes / bytes_per_slice) * slice_ms`，其中 `bytes_per_slice = sample_rate * channels * bytes_per_sample * slice_ms / 1000`。禁止默认写死 30s。

`finalize_started` 之后完成矩阵不再 Terminal（只许 Phase C 调一次 `terminalLocked`）。

### WP7 失败 JSON、CancelTurn、三段收口

失败 JSON（`Code=1` 或 `14007`）：

```text
持 device_mu
  acc = []
  acc += appendEventLocked(protocol_error)
  按取消表算 token
  持 conn_mu → writePump_mu → cr = CancelTurn → 释放泵与 conn
  if cr == backpressure:
    acc += appendEventLocked(local_validation_error, reason=stage3_backpressure)
  n = terminalLocked(turn_end_reason=error)
  acc += n
释放 device_mu
notify(--wait completion)
# 禁止 Close socket
```

进程退出 / 连接失败 → `request_finalize`（后来者 join `finalize_done`）。CLI 退出必须 wait `finalize_done`。

Phase A（已持 `device_mu`）：`finalize_started=true`；`BeginClose`（已 Terminal 则 token=无，**禁止清空** CancelTurn 已入队的 Stage=3）；`CloseResult=backpressure` 写入 `acc` 并在进 Phase B **前** notify（Phase 1 无 HTTP waiter，notify 是空循环，但 `acc` 不得丢给 Phase C）。

Phase B（无 `device_mu`）：drain keep，时限 `write_drain_timeout_sec`，关设备 socket，等读循环退出。

Phase C（再持 `device_mu`，无 IO）：若槽未 Terminal，`terminalLocked(connection_lost)` **一次**；写 `connection_stopped`（用户退出）或 `connection_failed`（异常）；broadcast `finalize_done`。禁止再调会自行加锁的 Terminal 包装。

SIGINT/进程退出走 BeginClose，**不要**当成矩阵里的 `interrupt`（本阶段无 `/interrupt`、无 `cmd/interrupt`）。取消表的 interrupt 行仍要在 `CancelTurn` 实现里写对，供失败 JSON 与 Phase 2 复用。

### WP8 录制

路径（契约）：

```text
recordings/{device_id}/{turn_id}/frames.jsonl
recordings/{device_id}/{turn_id}/uplink.pcm
recordings/{device_id}/{turn_id}/downlink.pcm
recordings/{device_id}/{turn_id}/turn.json
```

异步队列落盘。pcm 是原始 s16le，**不要**再包 RIFF。上行 pcm payload 不得出现 `52 49 46 46`。

字段 schema 正式文档未写，用下面「推断」默认（§7）。

### WP9 三个命令

见 §8。

### WP10 对照真实 core 的验收

见 §11。用 `-race` 跑单测。

---

## 5. 线格式清单（设备 ↔ core）

来源：`docs/toy-device-websocket-protocol.md`。MQTT / `'2'` / `'3'` 不做。

### 5.1 握手

| 项 | 值 |
|----|-----|
| Header `Device` | `{enterprise}/{device_type}/{device_id}` 恰好 3 段 |
| Header `Action` | `chatbot` |
| Query 兜底 | 协议允许；本阶段 **只发 Header**（推断） |

### 5.2 首字节

| 字节 | 方向 | 其后 |
|------|------|------|
| `'0'` | 双向 | 100B 头 + payload |
| `'1'` | 双向 | 管理 JSON |
| `'4'` | 上 | 28B ACK |
| `'{'` | 下 | 无前缀 JSON |

服务端剥首字节再处理；组帧必须自己加。

### 5.3 音频头要点

- Magic `Head=0x4848`，LE，2 字节 padding 必须存在
- 服务端取 payload 用 **帧长−100**（剥 `'0'` 之后），不是只信 `AudioPayloadLen`；头字段仍应填对
- 上行 `NeedAck=0`；下行可能为 1
- TTS 下行 Stage 恒为 1，**不保证** Stage=2
- UUID ∈ `[1, 0x7FFFFFFF]`，禁止 `int32` 负数

### 5.4 ACK（Phase 1）

仅当报文 `NeedAck==1` 或指令 `need_ack==1`（运行时看报文，不猜 Mongo）。

binary：`'4'` + 28 字节。`SleepMs=0`，`Code` 用配置（默认 0）。

| 下行种类 | `Ack` | `DownlinkType` |
|----------|-------|----------------|
| TTS | 音频 Seq | 1 |
| 提示音 | 音频 Seq | 2 |
| 指令 | 指令 `sequence_number`（缺省 0） | 3 |

`expect_downlink_need_ack: false` 只表示「不强制断言服务端一定要 ACK」；若实际帧带了 NeedAck，仍要回。

---

## 6. 事件（内存 log）

`event_seq` 从 1。每条含 `device_id`、`instance_id`、`event_seq`。连接级可带 `correlation_id`。Reserved 之后的对话事件带 `turn_id`。

禁止伪造服务端日志名：`device_not_found`、`status_invalid`、`no_active_turn` 等。静默丢包只能在 fault 矩阵 drop 行、且 first_reply 到期时发 `expected_server_drop`。

### 6.1 本阶段必须能写出

| 类型 | 何时 |
|------|------|
| `connected` | Dial 成功 |
| `registering` / `registered` | register 发送 / ACK code=0 |
| `reporting` / `ready` | 初始 report 发送 / 回显成功（skip_report 不发后两条） |
| `ack_failure` | register `data.code != 0` |
| `report_echo` / `report_echo_unmatched` / `report_timeout` | 见 WP5 |
| `local_validation_error` | 正常路径非法未出站；Stage=3 未入队时 `reason=stage3_backpressure` |
| `asr_result` | `Action=asr_result` |
| `command_received` | `'1'` + `/command/client` |
| `json_reply` | 无前缀 `Code==0` 且非 asr_result |
| `tts_chunk` | 匹配 UUID 的 `'0'` Stage=1 |
| `tts_done` | ≥1 帧匹配 TTS 且经 TTS idle 进 Terminal |
| `vad` | Stage=4 |
| `protocol_error` | Code=1/14007 或非法首字节 |
| `early_downlink_overflow` | 缓冲 32 溢出 |
| `expected_server_drop` | **仅** fault 矩阵 drop 行 + 完成矩阵 timeout |
| `turn_terminal` | 必带 `turn_id`、`turn_end_reason`、`uplink_end_reason`、`reply_kind` |
| `connection_stopped` | 用户/CLI 主动收口且本趟关了连接；不设 last_error |
| `connection_failed` | 仅异常收口；Phase C 之后；带 reason |

### 6.2 本阶段不要写

`device_deleted`（没有 delete）。  
`inferred_no_reply` 是 `expected_server_drop` 的别名，只保留一个主类型。

事件 JSON 外形推断：

```json
{
  "device_id": "sim_001",
  "instance_id": "ins_...",
  "event_seq": 1,
  "event_type": "turn_terminal",
  "ts": "2026-08-22T12:00:00.000Z",
  "turn_id": "...",
  "turn_end_reason": "idle",
  "uplink_end_reason": "stage2",
  "reply_kind": "tts"
}
```

Phase 1 不对外暴露 `GET /events`；CLI `--wait` 成功后把关键事件打到 stderr/stdout 即可（推断：`--wait` 结束打印 `turn_terminal` 一行 JSON）。

---

## 7. 落盘 schema（推断；正式文档只给了路径）

实现按此写，避免两套文件对不齐。Phase 2 HTTP 读同一结构时再加 `instance_id` 目录，字段尽量不改。

### 7.1 `frames.jsonl` 每行

```json
{
  "ts": "2026-08-22T12:00:00.123Z",
  "direction": "outbound",
  "first_byte": "0",
  "topic": "",
  "stage": 1,
  "seq": 0,
  "uuid": 123,
  "need_ack": 0,
  "payload_len": 3200,
  "header_len": 100,
  "payload_sha256": "...",
  "injected_fault": ""
}
```

| 字段 | 规则 |
|------|------|
| `direction` | `outbound` / `inbound` |
| `first_byte` | `"0"` `"1"` `"4"` `"{"` |
| `header_len` | `bad_header` 必须能看出 **< 100** |
| 上行音频 payload | 不得含 RIFF magic |
| 注入坏帧 | 必须出现在 outbound（测的是服务端静默丢，不是本地拦截） |

### 7.2 `turn.json`

```json
{
  "device_id": "sim_001",
  "instance_id": "ins_...",
  "turn_id": "...",
  "uplink_uuid": 123,
  "injected_fault": "",
  "uplink_end_reason": "stage2",
  "turn_end_reason": "idle",
  "reply_kind": "tts",
  "seq_before": 0,
  "started_at": "...",
  "ended_at": "..."
}
```

`turn_end_reason` / `reply_kind` 必须能用 §6 终止表对上。

### 7.3 pcm

- `uplink.pcm` / `downlink.pcm`：拼接后的原始 pcm，无 WAV 头
- 配置 `save_uplink_audio` / `save_downlink_audio` 为 false 则不写对应文件
- `enable_frame_log` 为 false 则不写 `frames.jsonl`

---

## 8. CLI

### 8.1 `cmd/check`

```bash
go run ./cmd/check --config configs/example_device.yaml
```

只校验 YAML + §2 拒绝规则。不连 WS。合法 → 退出 0。

### 8.2 `cmd/fixture`

```bash
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
```

`device_id = sim_sr_{run_uuid}_{n}`。`n` 对该 `run-id` 在 out 文件中已有行数 + 1（推断）。**追加** jsonl，不覆盖其它 run。

行格式推断：

```json
{"run_id":"...","n":1,"device_id":"sim_sr_<run_id>_1"}
```

skip_register 必须用夹具新 ID（服务端无预注册），写入该 jsonl。

### 8.3 `cmd/speak`

```bash
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/audio/hello.wav --wait
go run ./cmd/speak --config ... --wait --inject skip_register --device-id "$(夹具id)"
```

| 参数 | 含义 |
|------|------|
| `--config` | 设备 YAML |
| `--audio` | WAV 路径；仅 Phase 1 有此参数 |
| `--wait` | 等 `turn_terminal`（completion），预算见 WP6 |
| `--inject` | 见 §9；可空 |
| `--device-id` | 覆盖 YAML 的 `device_id`（skip_register 用） |

本阶段 **没有** `cmd/start` 守护进程、没有 `cmd/interrupt`、没有 HTTP。一个进程做完即退出。

退出码推断（正式文档未写，建议钉死）：

| 情况 | 退出码 |
|------|--------|
| `turn_end_reason=idle`（含 silent / 仅 command / 仅 json） | 0 |
| `--inject` 为 drop 行且出现 `expected_server_drop` | 0 |
| `timeout`（正常路径、非 drop 行） | 2 |
| `error` / 配置拒绝 / WAV 非法 / 未 speakable | 1 |
| `connection_lost` / Dial 失败 | 1 |

---

## 9. fault 注入

CLI `--inject`，一次一个。坏帧必须真正写出（进 `frames.jsonl` outbound）。

| fault | 出站 | 期望 | speakable 停在 |
|-------|------|------|----------------|
| `skip_register` | 不 register；合法音频 + Stage=2 | `expected_server_drop` | Connected |
| `skip_report` | 不 report；合法音频 | **不**默认 drop；不断言 timeout 为通过 | Registered |
| `bad_seq` | CAS 后 Seq≥1 的 Stage=1 | drop | Ready |
| `oversize` | payload > 51200 | drop | Ready |
| `bad_header` | `'0'` + 其后不足 100 字节 | drop | Ready |
| `dup_uuid` / `bad_stage` | 可录帧 | **不作** drop 验收 | Ready |

`skip_report` 成功长什么样：正式文档没钉。建议验收为：进程能发完音频、连接未因本地错误断开；若 core 仍回 TTS 则按完成矩阵正常结束；若无回复则 `timeout` **不算失败用例**（不要标 `expected_server_drop`）。

---

## 10. 锁、并发、收口备忘

```text
device_mu → conn_mu → report_mu
CancelTurn / BeginClose 在持 conn_mu 时再取 writePump_mu
```

- 泵协程不回锁 `device_mu`/`conn_mu`
- `terminalLocked` / `appendEventLocked`：调用方已持 `device_mu`；函数内不再加锁、不 IO、不 Enqueue、不唤醒
- 唤醒 `--wait`：解锁之后
- 同一临界区多次 `appendEventLocked` 必须把 `EventNotify` **累加**到 `acc`（Phase 1 虽无 HTTP，也不许丢返回值乱删 waiter 表）
- closer 独立协程；读循环不负责 Close
- `write_queue_depth` 默认 256，必须 ≥ 2

---

## 11. 验收对照

### 11.1 交付（phase1.md §10.1）

- [ ] 握手 Device 三段 + `Action=chatbot`
- [ ] register `code=0`
- [ ] 初始 report 后 Ready
- [ ] pcm `Seq=0`，Stage 1→2
- [ ] 收到匹配 TTS **或** 矩阵内其它成功 `reply_kind`
- [ ] `--wait` 预算内结束（含加长 `post_final_asr_silence` 的 silent）
- [ ] 落盘四件套（按配置开关）
- [ ] `check` / `fixture` / `speak` 可运行
- [ ] keepalive：Ready 后空闲发 report，**设计上**撑过 360s；自动化可测「keepalive 定时器会 Enqueue report」，6 分钟实连作为手工/夜间项，不要卡在每次 CI
- [ ] 读循环不因写盘阻塞（单测：recorder 慢时读仍前进）
- [ ] WAV 与配置 audio_* 不符 → 拒绝，不占槽
- [ ] `skip_register`：夹具 jsonl + `expected_server_drop`
- [ ] `skip_report` 不要求 drop
- [ ] 退出 `Disconnected`，wait 过 `finalize_done`

### 11.2 正确性（phase1.md §10.2）

- [ ] register ACK∥timeout 只消费一次
- [ ] 失败 JSON：视 `CancelResult` 发 Stage=3，**连接未因 CancelTurn 关闭**
- [ ] `backpressure` 必有 `local_validation_error`/`stage3_backpressure`
- [ ] 进程退出 BeginClose **过滤**未发 Stage 1/2
- [ ] 已 Terminal 时 token=无 **禁止清空** 已入队 Stage=3；Phase B 写出它
- [ ] `terminalLocked` 不在锁内唤醒
- [ ] 解码失败不占槽
- [ ] 全部终态有 `turn_terminal`，字段符合终止表

### 11.3 建议补的单测（文档没列命令，但是契约边界）

- [ ] golden `AudioHeader` 100 字节
- [ ] 队列 depth=2 数据满仍能入 1 帧 Stage=3
- [ ] BeginClose keep 只含 Stage=3
- [ ] `early_downlink_buf` 回放不二次 ACK
- [ ] Stage=4 在 WaitingReply 前：vad + 补 Stage=2，不 Terminal

---

## 12. 仍不要自行发明的点

下列正式文档留空，上面用了「推断」。若要改默认，先改本文再写代码，不要静默分叉。

1. `turn.json` / `frames.jsonl` / `fresh_ids.jsonl` 字段名  
2. `cmd/speak` 退出码  
3. report 的 `signalStrength` / `batPowerLevel` 默认值  
4. 设备泵是否对每帧 `SetWriteDeadline`  
5. `instance_id` 字符串格式  
6. keepalive 的 360s 是 CI 必跑还是手工  
7. `skip_report` 无 TTS 时算不算 CLI 成功  
8. SIGINT 映射为 BeginClose（建议）还是 interrupt（不建议，无 CLI 入口）

---

## 13. 最小主路径时序（对照用）

```text
speak 进程
  load YAML → check 拒绝规则
  Dial Header Device/Action
  register（timer 先于发送）
  report seq=report_sequence_start
  读 WAV → PCM → CAS Reserved
  发 '0' Stage=1 Seq=0..n-1
  发 '0' Stage=2
  读循环：TTS / command / JSON / asr_result / VAD
  WaitingReply 计时 → terminalLocked
  --wait 返回
  request_finalize → Phase A BeginClose → B drain → C
  进程退出 0
```
