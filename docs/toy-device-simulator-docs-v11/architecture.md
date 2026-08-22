# 玩具设备模拟器 — 整体架构设计文档（v11）

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：

1. **忠实模拟** 真实玩具设备的 chatbot 协议行为
2. **批量设备并发测试**（模板、错峰、限额）
3. 参数可配置、可保存
4. **Agent 可编程 API**（REST + 事件 WS）
5. **人工调试 UI**（配置 + 简单对话）

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。主路径：`Action=chatbot`。

**每阶段做完即可交付使用。** Phase 1 的交付物是能跑通的单设备 CLI，不是一份竞态备忘录。

## 2. 设计原则

1. **协议忠实**：对齐协议与基线 `types.AudioHeader`。`example/asr/mock.go` 仅缺陷清单，禁止作 golden。
2. **失败按 fault 矩阵推断**，禁止伪造服务端日志原因名。
3. **Turn 一等公民**：拆分 `uplink_end_reason` / `turn_end_reason`；先写不改。
4. speak 受理时 CAS Reserved。
5. **API 先于 UI**。
6. Phase 1/2 线上 **pcm**（mono s16le）。WAV 只作源文件。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK **只看报文** `NeedAck` / `need_ack`。
9. `device_id` 创建后不可变。
10. 推荐实现语言 **Go**（按字节收 TextMessage 中的二进制）。

审查纠错（保留，不退回 v1）：提前下行只缓存、report 锁内取号、register **先登记 timer 再发送**、仅 IsFinal 可 silent、出站串行化、限额原子闸门。

## 3. 总体架构

```text
Control Plane: Web UI | REST + 事件 WS | CLI / Scenario / cmd/fixture
Device Manager: 生命周期 / 模板批量 / stagger
                conn_permit + speak_permit（原子闸门）
                Turn 槽 CAS / fail_connection
DeviceInstance:
  writePump（唯一出站入口）
  读循环（不阻塞落盘）
  keepalive（周期 report）+ last_activity
  pending_reports / early_downlink_buf / event_log
  Connection + UplinkTurn + DownlinkPlayer
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid
├── state     # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── injected_fault
├── uplink_end_reason   # 空 | stage2 | interrupt | vad | error | timeout；非空不改
├── turn_end_reason     # idle | interrupt | error | timeout | connection_lost
├── reply_kind          # 空 | tts | command | json | silent | command+tts | json+tts
├── related / early_downlink_buf
├── first_reply_timer / settle_timer
├── chunks / asr_results[] / commands[] / json_replies[] / events[]
```

- 每设备一槽。speak / speak_and_wait 受理时 CAS Reserved；后者与 waiter **同锁注册**。
- 默认 reject → 409。禁止用「第一帧是否已发」判断占用。
- **WaitingReply 之前**禁止因 command / 成功 JSON / 匹配 TTS 将 Turn 置 Terminal。

`uplink_end_reason`：Stage=4 立即 `vad`（若空）再补 Stage=2；已非空则后续 Stage=2/取消不得覆盖。Reserved 取消：保持空。连接失败且套接字已死：若空则 `error`。

### 4.2 Event（完整表，不引用其它版本）

每条事件有 `device_id` 与单调 `event_seq`（从 1）。连接级用 `correlation_id`（与是否有 Turn 无关）。Reserved 之后的对话事件用 `turn_id`。

禁止声称所有事件都有 `turn_id`。禁止发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志名。

| 类型 | 含义 |
|------|------|
| `connected` / `registering` / `registered` / `reporting` / `ready` | 连接生命周期 |
| `local_validation_error` | 正常路径非法，帧未发出 |
| `ack_failure` | `'1'` + `/register/client` 且 `data.code != 0`；随后 `fail_connection` |
| `report_echo` | `/report/client` 的 `ReportData` 命中 `pending_reports[seq]` |
| `report_echo_unmatched` | 回显序号不在表中 |
| `report_timeout` | pending 项超时 |
| `connection_failed` | 收口完成且实例已 **Stopped** 之后写入；reason 见 §4.11 |
| `asr_result` | 无前缀 JSON `Action=asr_result`（含 interim；完成判定只认 IsFinal） |
| `command_received` | `'1'` + `/command/client` |
| `json_reply` | 无前缀 `Code==0` 且非 asr_result |
| `tts_chunk` | 匹配 UUID 的 `'0'` Stage=1 |
| `tts_done` | ≥1 帧匹配 TTS，且本次经 TTS idle 进入 Terminal |
| `vad` | 收到 Stage=4 |
| `expected_server_drop` | WaitingReply 首包到期；无终态相关下行；无匹配且 **IsFinal=true** 的 asr_result。仅矩阵 drop 行作为通过 |
| `protocol_error` | `Code=1` / `14007` 或非法首字节 |
| `early_downlink_overflow` | 提前下行缓冲溢出 |

`inferred_no_reply` 是 `expected_server_drop` 的别名，实现只保留一个主类型。

**event_log：** 默认保留最近 **10000** 条或 **24h**（先到为准）。`after_event_seq` 小于日志中最旧序号 → **410**（或 409）`event_seq_expired`，body 含 `oldest_seq` / `newest_seq`。省略游标的 wait 只等未来事件。

### 4.3 相关下行、提前缓存、终止矩阵

基线可不下发 TTS：`SyncDevice` 纯 command；`ReplyModeText` 成功 JSON；空 `replyText` 无包。

**关联**

| 种类 | 形态 | 本轮判定 |
|------|------|----------|
| TTS | `'0'` | UUID == uplink_uuid |
| command | `'1'` `/command/client` | 无 UUID；Turn 已 Speaking（发过 `'0'`）或之后 |
| asr_result | `{` Action=asr_result | SessionID == uuid 十进制；记录 IsFinal |
| 成功 JSON | `{` Code==0，非 asr_result | 同 command 占用 |
| 失败 JSON | Code=1 / 14007 | 占用；立即停上行 |

**首次收到时（含 WaitingReply 前）：** 发事件、按需 ACK、录帧、落盘。写入 `early_downlink_buf`（若尚未 WaitingReply）。容量默认 **32** 条；溢出丢最旧并 `early_downlink_overflow`。

**WaitingReply 前：** 不启动完成计时、不 Terminal、不释放槽。失败 JSON 除外（取消表停上行）。发送侧每帧 Stage=1 前检查 `state != Terminal`。

**进入 WaitingReply 后回放缓冲：只驱动计时状态**（取消 `first_reply_timer`、启动 settle 等）。**禁止**第二次 ACK、第二次事件、第二次录帧、第二次音频落盘。然后清空缓冲。

**计时（仅 WaitingReply 起）**

| 计时器 | 启动 | 规则 |
|--------|------|------|
| first_reply | 进入 WaitingReply | 终态下行取消；默认 20s |
| TTS idle | 匹配 TTS | 后续 TTS 重置 20s |
| 非音频 followup | command/成功 JSON 且尚无 TTS | 默认 5s；其后有 TTS 则改 idle |
| post_final_asr_silence | first_reply 到期 **且** 已有 **IsFinal=true** 且无终态下行 | 默认 5s → silent idle。interim-only **不得**启动 |

**终止：** 仅 TTS → idle + `tts_done`；仅 command/JSON → followup 后 idle、无 `tts_done`；command+TTS → TTS idle；仅 IsFinal 无终态 → silent idle；仅 interim 或全无 → timeout / drop 行 `expected_server_drop`；失败 JSON → error。

完全无包的静默成功与 drop 不可区分：正常路径 timeout。探针 Phase 4。

等待预算：`upload + first_reply + max(idle, followup) + slack`。禁止默认写死 30s。

### 4.4 Scenario

TTS 用例：`tts_done` + idle。纯指令：`command_received`。文本：`json_reply`。`asr_contains` 仅 `StreamingAsrTextReply`，optional。

### 4.5 注入矩阵

不查 Mongo。`skip_register`：夹具 `sim_sr_{run_uuid}_{n}`，本趟不预注册，写 `fresh_ids.jsonl`，**新建**实例。

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1；服务端无 ASR session | drop |
| oversize / bad_header | payload>51200 / `'0'`+<100B | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

### 4.6 取消表

套接字已死则跳过出站。`uplink_end_reason` 非空不改写。

| 状态 | 出站 | 空的 uplink_end_reason | turn_end_reason |
|------|------|------------------------|-----------------|
| Reserved | 不发 Stage=3 | 保持空 | interrupt / connection_lost |
| Speaking | Stage=3；停 Stage=1 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；可 Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | 可 Stage=3 | 保持 | 同上 |
| WaitingReply | 可 Stage=3；取消计时器 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

### 4.7 report 取号

`report_next_seq` 初值 = `report_sequence_start`（默认 1）。**第一个序号等于 start。**

同一把 `report_mu`：取号、递增、登记 `pending_reports[seq]`，再经 **writePump** 发送。或 `atomic.AddUint64`（初值 start-1）+ 锁/`sync.Map` 插入 pending。禁止无锁读改写、禁止无锁 Go map。

仅 `kind=initial` 且 Reporting → Ready。initial 超时 → `fail_connection(report_timeout)`。keepalive 与手动 report 共用此路径。

### 4.8 出站串行化（writePump）

每个 DeviceInstance **唯一**出站入口：`write_mu` 或 `writePump` 队列。

必须经此入口：register、report（含 keepalive）、音频 Stage=1/2/3、binary/JSON ACK、其它管理帧。

同一 Turn 的 Stage=1 各片与最后 Stage=2 **按入队顺序写出**（由单一上行协程按序 `Enqueue`）。写失败 → `fail_connection(write)`。

`report_mu` 只保护取号+pending，**不**代替 writePump。

### 4.9 ACK

运行时只看报文标志。Phase 1：音频+指令 binary，SleepMs=0。Phase 2：json、非零 SleepMs、节流 A/B/C。

`sleep_ms: 0` 不写节流缓存、不能清旧值。正值用例必须不同 `device_id`。

**音频 binary：** Ack=音频 Seq；DownlinkType TTS=1 / 提示音=2；Code/SleepMs=配置。  
**音频 JSON：** ack 与 sequence_number=音频 Seq；downlink_type=`tts`/`hint_audio`；uuid=音频 UUID；sleep_ms/code=配置。  
**指令 binary：** Ack=指令 sequence_number（缺省 0）；DownlinkType=**3**。  
**指令 JSON：** ack 与 sequence_number=指令序号；downlink_type=`command`；topic=原指令完整 topic；uuid 省略或 0；sleep_ms/code=配置。

binary：`'4'`+28 字节。json：`'1'` + `.../downlink-ack/server`。

A/B/C：**同一 device_type** 且 DownlinkAck=true；三台不同 ID；status=1；TTS≥2 片。A=binary/0，B=binary/500，C=json/500。

### 4.10 身份与 playingMode

`device_id` 创建后不可变。PUT/faults 改 ID → 400。skip_register 用夹具 ID 新建。

**热更新：** `playingMode`（及同类少数字段）可通过 `POST /report` 在 **Ready** 时热更，keepalive report 带当前值。  
**必须重连：** enterprise、device_type、device_id。Running 时改这些 → 409。

### 4.11 连接失败、register 时序、实例 Running 映射

**register 发送（禁止先发后装 timer）：**

1. 持 `conn_mu`：Connection=`Registering`；登记 `register_ack` waiter；启动 `register_ack_timeout_sec`（默认 5）。
2. 释放锁（或保持锁但不发送 IO）。
3. 经 writePump 发送 register。
4. 任意 `/register/client`：**持同一把锁**取消 timer。`code==0` → Registered；`code!=0` → `ack_failure` 再 `fail_connection(register_nack)`。

`skip_register` 不发送、不装该 timer。

**实例状态 vs Connection：**

| 路径 | Connection | 实例变为 Running 的时机 | speak 前置 |
|------|------------|-------------------------|------------|
| 正常 | Ready | Ready 达成时 Starting→Running | Running 且 Ready |
| skip_register | 停在 Connected | Connected 达成时 Starting→Running | Running 且 Connected |
| skip_report | 停在 Registered | Registered 达成时 Starting→Running | Running 且 Registered |

GET 同时返回 `instance_state` 与 `connection_state`。禁止注入路径永远停在 Starting。

**fail_connection 顺序（幂等）：**

1. → Stopping；取消全部 timer。
2. 槽按取消表；清空 pending。
3. 关 WS；Connection=Disconnected。
4. **释放 conn_permit（恰好一次）**；实例 → **Stopped**（`last_error`）。
5. 写入 `connection_failed`（此时 start 已不会因 Starting 而 409）。
6. 唤醒 waiter。

触发：handshake；register_timeout / nack；read/write/remote_close；report_send / report_timeout。可再 start。

### 4.12 keepalive 与 last_activity

- `keepalive_interval_sec` 默认 **60**，方法为周期 **report**（避开 register 每秒限流）。
- 仅 Ready 之后启动（skip_report 不停 Ready，不发 keepalive）。
- 暴露 `last_activity`（任意入站或成功出站刷新）。
- 服务端约 **360s** 空闲踢线：有 keepalive 时连接应保持。

### 4.13 读循环不阻塞落盘

独立读协程持续读全部下行。指令 / ASR / TTS 头 **立即**进事件总线。帧录制、PCM 落盘、解码放工作队列或独立协程，**禁止**在读协程里同步写盘。

### 4.14 批量与原子限额

| permit | 容量 | 获取 | 释放（恰好一次） |
|--------|------|------|------------------|
| `conn_permit` | `max_connections` | start 进入 Starting 前 TryAcquire；失败 **429**、状态不变 | Stopped / Deleted / fail_connection |
| `speak_permit` | `max_concurrent_speaking` | 与 CAS Reserved **同一成功路径**内 TryAcquire；CAS 失败则 **回滚**已拿到的 permit | 该 Turn Terminal（含取消/连接失败） |

禁止「先读计数再 ++」而无闸门。并发 start/speak 超限测试必须写。`default_stagger_ms` 默认 50。批量创建冲突 **整批 409**。`POST /devices/batch/start|stop|delete`。

## 5. 协议硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text 帧，禁止 UTF-8 解码下行音频 |
| AudioHeader | 100 字节含 padding；golden 对照基线；禁止 mock.go |
| 占用 | 受理时 CAS Reserved |
| 新一轮 | Seq=0 + Stage=1；禁止 MH 前缀机型作示例 |
| Stage=4 | 立即记 vad（若空），停 Stage=1，补 Stage=2 |
| 下行结束 | 无稳定 Stage=2；完成矩阵见 §4.3 |
| **心跳** | 周期 report；`last_activity`；防 360s 空闲踢线 |
| **读循环** | 持续读；指令立即进事件总线；**不因写文件/解码阻塞** |
| **热更新** | playingMode 经 report；身份字段必须重连 |
| Register | 先登记超时再发送；`code==0` 才 Registered |
| Report | 锁内取号；首序号=start；匹配回显才 Ready |
| 线上格式 | pcm s16le mono |
| ACK | 只看报文标志 |
| 出站 | 全部走 writePump |
| 配置 | Device 三段、playing_mode ∈ {1,2,3}、UUID 范围 |

## 6. 音频管线

源 WAV 解 RIFF → 内部 PCM → silence=全零采样 → 按 slice_ms 切 **pcm 字节**。禁止逐片封装 WAV。Phase 4 才允许线上 mp3/wav（整段只编码一次）。

## 7. 技术选型

硬门槛：按字节收 TextMessage 二进制。Phase 1 **推荐 Go**。真实 core 收到至少一帧 `'0'` TTS 且未因 UTF-8 失败后锁定。

## 8. 阶段（每阶段可独立交付）

| 阶段 | 交付定义（做完就能用） |
|------|------------------------|
| Phase 1 | 单设备：握手→register→report→pcm 上行→收下一条回复；CLI；落盘；keepalive |
| Phase 2 | 批量+API+Scenario；playingMode 热更新；JSON ACK |
| Phase 3 | Web UI 只消费 Phase 2 |
| Phase 4 | queue、非 pcm、静默探针 |

## 9. 目录

`protocol/` `core/`（writePump、pending_reports、early_downlink、event_log）`cmd/speak|check|fixture/` `configs/example_device.yaml` `configs/templates/` `testdata/`

## 10. 对齐基线

仓库 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。  
register Redis 出错可不 ACK。管理消息独立 goroutine。MH Seq 例外。不要用 `projects/go/ai-creates-wealth`。
