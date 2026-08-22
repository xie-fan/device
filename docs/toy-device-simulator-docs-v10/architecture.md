# 玩具设备模拟器 — 整体架构设计文档（v10）

设备只响应报文上的 ACK 标志，不查询服务端 `DownlinkAck`。  
`device_id` 创建后不可变。Turn 完成按相关下行矩阵；**上行未进入 WaitingReply 前不因 command/JSON/TTS 释放槽**。

## 1. 背景与目标

模拟 WebSocket `Action=chatbot`：单设备闭环、**批量并发**、可配置、Agent API、调试 UI。

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。

## 2. 设计原则

1. 协议忠实；mock.go 仅缺陷清单。
2. 失败按 fault 矩阵推断。
3. `uplink_end_reason` 先写不改。
4. speak 受理时 CAS Reserved。
5. API 先于 UI；waiter 与状态检查同一临界区。
6. Phase 1/2 线上 pcm。
7. Connection / UplinkTurn / DownlinkPlayer 正交。
8. ACK 只看报文标志。
9. report：**锁内**取号+递增+登记 pending；首个序号 = `report_sequence_start`。
10. `device_id` 不可变。
11. Starting 必须收口：register 超时或拒绝 → `fail_connection` → Stopped。
12. 仅 `asr_result.IsFinal==true` 可走 silent；interim 不算成功。

## 3. 总体架构

```text
Control Plane: UI | REST+WS | CLI / Scenario / cmd/fixture
Device Manager: 生命周期 / 模板批量 / stagger / max_connections
                / max_concurrent_speaking / Turn 槽 CAS / fail_connection
DeviceInstance: Connection + pending_reports + early_downlink_buf
                + UplinkTurn + DownlinkPlayer + event_log
protocol: AudioHeader / '1' / '4' / '{'
recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid / state
├── injected_fault
├── uplink_end_reason / turn_end_reason / reply_kind
├── related / early_downlink_buf     # 上行未收口前只缓存
├── first_reply_timer / settle_timer
├── uplink_chunks[] / downlink_chunks[]
├── asr_results[]  commands[]  json_replies[]  events[]
```

状态：Reserved | Speaking | FinishingUpload | WaitingReply | Terminal。

- 每设备一槽。speak / speak_and_wait CAS 创建 Reserved；后者与 waiter **同锁注册**。
- reject → 409。禁止用「第一帧是否已发」判断占用。
- **WaitingReply 之前禁止因 command / 成功 JSON / 匹配 TTS 将 Turn 置 Terminal。**

### 4.2 `uplink_end_reason`（先写不改）

| 触发 | 若空 | 若非空 |
|------|------|--------|
| Stage=4 | 立即 `vad`，停 Stage=1，补 Stage=2 | 保持 |
| 发出 Stage=2 | `stage2` | 保持 |
| 本地 Stage=3/5 | `interrupt` | 保持 |
| 取消表 | Reserved 保持空；其余见 §4.7 | 保持 |
| fail_connection 且套接字已死 | `error` | 保持 |

连接失败时 `turn_end_reason=connection_lost`。

### 4.3 Event

每条事件有 `device_id` 与单调 `event_seq`（从 1）。**先写入 event_log，再唤醒 waiter。**

| 事件 | 关联键 |
|------|--------|
| 握手、register/report 类、`connection_failed` | `correlation_id` |
| Reserved 之后的对话事件 | `turn_id` |

| 类型 | 含义 |
|------|------|
| `expected_server_drop` | WaitingReply 首包到期；无终态相关下行；**无**匹配 SessionID 且 **IsFinal=true** 的 asr_result。仅矩阵 drop 行作为通过 |
| `ack_failure` | `/register/client` 且 `data.code != 0`；随后必须 `fail_connection` |
| `connection_failed` | `fail_connection`；reason 含 handshake / register_timeout / register_nack / read / write / remote_close / report_send / report_timeout |
| `asr_result` | `Action=asr_result`（含 interim） |
| `command_received` / `json_reply` / `tts_done` / `report_echo` 等 | 同前版语义；`tts_done` 仅 TTS idle |

禁止伪造服务端日志原因名。

### 4.4 相关下行：关联、缓存与终止

基线可不下发 TTS：`SyncDevice` 纯 command；`ReplyModeText` 成功 JSON；空 `replyText` 无包。

#### 4.4.1 关联

| 种类 | 形态 | 判为本轮 |
|------|------|----------|
| TTS | `'0'` | UUID == uplink_uuid；其它 UUID 不计入完成 |
| command | `'1'` `/command/client` | 无 UUID；槽内 Turn 已 Speaking（发过 `'0'`）或之后 |
| asr_result | `{` Action=asr_result | SessionID == uuid 十进制字符串；记录 IsFinal |
| 成功 JSON | `{` Code==0，非 asr_result | 同 command 占用 |
| 失败 JSON | Code==1 / 14007 | 占用；`protocol_error` |

#### 4.4.2 提前到达：只缓存，不完成

Turn 处于 Reserved / Speaking / FinishingUpload 时：

- 匹配的 TTS / command / 成功 JSON / asr_result **写入 `early_downlink_buf` 与 related**，发对应事件。
- **不**启动 `first_reply_timer` / settle / `post_final_asr_silence`。
- **不** Terminal，**不**释放槽，上行继续直到 Stage=2、VAD 补 Stage=2、或取消表。
- **失败 JSON**：立即按 §4.7 停上行（可发 Stage=3），Terminal `error`。这是明确的「停上行」转移。
- 发送协程每发一帧 Stage=1 前检查 `state != Terminal`；已 Terminal 必须停发。

进入 **WaitingReply**（Stage=2 已发出）时：

1. 启动 `first_reply_timer`（默认 20s）。
2. **回放** `early_downlink_buf`：等价于这些包在 WaitingReply 刚发生时到达（取消首包、启动 settle 等），然后清空缓冲。
3. 之后实时包按 §4.4.3。

无 UUID 的外部 command 在长音频期间只入缓冲，不会让槽先于上行结束而空出来。

#### 4.4.3 计时（仅 WaitingReply 及之后）

| 计时器 | 启动 | 重置 / 取消 |
|--------|------|-------------|
| `first_reply_timer` | 进入 WaitingReply | 第一条终态相关下行（TTS / command / 成功或失败 JSON）；含缓冲回放 |
| TTS idle | 匹配 TTS | 后续匹配 TTS 重置 `downlink_idle_timeout_sec`（20） |
| 非音频 followup | command 或成功 JSON 且尚无匹配 TTS | 重置 `non_audio_followup_sec`（5）；其后若有 TTS 则改 TTS idle |
| `post_final_asr_silence` | **仅当** first_reply 到期 **且** 已有匹配 `asr_result` 且 **IsFinal=true** 且仍无终态下行 | 到期 → silent idle。**禁止** interim-only 启动本计时器 |

#### 4.4.4 终止矩阵

| 观察到的相关下行 | reply_kind | Terminal | turn_end_reason |
|------------------|------------|----------|-----------------|
| 仅匹配 TTS | tts | TTS idle | idle + `tts_done` |
| 仅 command | command | followup 到期无 TTS | idle，无 tts_done |
| 仅成功 JSON | json | 同上 | idle |
| command+TTS / json+TTS | 组合 | TTS idle | idle + 对应事件 + tts_done |
| **仅 IsFinal=true 的 asr_result**，无终态下行 | silent | first_reply 到期后 `post_final_asr_silence` | idle |
| **仅 interim**（IsFinal=false）或无 asr、无终态 | 空 | first_reply 到期 | timeout；drop 行另发 `expected_server_drop`。**不得 idle** |
| 失败 JSON | — | 立即（可在上行未结束时停发） | error |
| 取消表 | 保持 related | 立即 | interrupt |
| fail_connection | 保持 | 立即 | connection_lost |

设备完全无包的静默成功与 drop 不可区分：正常路径 timeout；drop 行 expected_server_drop。探针 Phase 4。

`tts_done`：≥1 帧匹配 TTS 且本次经 TTS idle Terminal。

#### 4.4.5 等待超时预算

```text
wait_timeout_sec >= upload_sec
                  + first_reply_timeout_sec
                  + max(downlink_idle_timeout_sec, non_audio_followup_sec)
                  + wait_timeout_slack_sec
```

禁止默认写死 30s。省略 `timeout_sec` 时服务端按上式计算。

### 4.5 Scenario

断言随预期回复：TTS → `tts_done`；纯指令 → `command_received`；文本 → `json_reply`。  
`asr_contains` 仅 `StreamingAsrTextReply`，optional。注入按 §4.6。

### 4.6 注入矩阵

speak 不查 Mongo。`skip_register`：夹具 `sim_sr_{run_uuid}_{n}`，本趟不预注册，写 `fresh_ids.jsonl`，Manager **新建**实例。

| fault | 出站 | 期望 |
|-------|------|------|
| skip_register | 不 register；合法音频+Stage=2 | expected_server_drop |
| skip_report | 不 report；合法音频 | 不默认 drop |
| bad_seq | CAS 后 Seq>=1；服务端无 ASR session | drop |
| oversize / bad_header | payload>51200 / `'0'`+<100B | drop |
| dup_uuid / bad_stage | 可录帧 | 不作 drop 验收 |

### 4.7 取消表

套接字已死则跳过出站。`uplink_end_reason` 非空不改写。

| 状态 | 出站 | 空 uplink_end_reason | turn_end_reason |
|------|------|----------------------|-----------------|
| Reserved | 不发 Stage=3 | 保持空 | interrupt / connection_lost |
| Speaking | Stage=3；停 Stage=1 | interrupt / error | 同上 |
| FinishingUpload Stage=2 未发 | 不发 Stage=2；可 Stage=3 | interrupt / error | 同上 |
| FinishingUpload Stage=2 已发 | 可 Stage=3 | 保持 | 同上 |
| WaitingReply | 可 Stage=3；取消计时器 | 保持 | 同上 |
| Terminal | 无 | 不变 | 不变 |

### 4.8 report 取号（必须原子）

回显可乱序。`report_next_seq` 初值 = `report_sequence_start`（默认 1）。**第一个发出的序号等于 start。**

**登记路径（二选一，禁止第三种）：**

1. **同一把 `report_mu`：** `seq = report_next_seq`；`report_next_seq++`；`pending_reports[seq] = waiter`。解锁后再发送。
2. **`seq = atomic.AddUint64(&counter, 1)`**，counter **初值 = start-1**（start≥1），Add 返回值即本次序号；再在 pending 锁或 `sync.Map` 中插入。禁止无锁 Go map。

禁止无锁「读当前值再 +1」。  
并发 keepalive 与 `POST /report` 不得得到相同 seq，不得覆盖对方 pending。

仅 `kind=initial` 且 Reporting → Ready。未命中 → `report_echo_unmatched`。initial 超时 → `fail_connection(report_timeout)`。keepalive 仅 Ready 后。

### 4.9 ACK

运行时：NeedAck==1 或 need_ack==1 必须 ACK。禁止查 DownlinkAck。

| 阶段 | 交付 |
|------|------|
| Phase 1 | 音频+指令 **binary**，sleep_ms=0 |
| Phase 2 | json mode、非零 SleepMs、节流 A/B/C |

`sleep_ms: 0` 不写缓存、不能清旧值。正值用例必须不同 device_id。

#### 音频 ACK

| binary | 取值 |
|--------|------|
| Ack | 音频 Seq |
| DownlinkType | TTS=1；提示音=2 |
| Code / SleepMs | 配置 |
| 内存字段 | 0 |

JSON（Phase 2）：

| 字段 | 取值 |
|------|------|
| ack | 音频 Seq |
| downlink_type | `tts` / `hint_audio` |
| uuid | 音频头 UUID |
| sequence_number | 音频 Seq |
| sleep_ms / code | 配置 |
| topic | 可选 |

#### 指令 ACK

| binary | 取值 |
|--------|------|
| Ack | 指令 `sequence_number`（缺省 0） |
| DownlinkType | **3** |
| Code / SleepMs | 配置 |
| 其余 | 0 |

JSON（Phase 2）：

| 字段 | 取值 |
|------|------|
| ack | 指令 `sequence_number` |
| downlink_type | `command` |
| sequence_number | 指令 `sequence_number` |
| topic | 原指令完整 topic（`.../command/client`） |
| uuid | 省略或 0 |
| sleep_ms / code | 配置 |

binary：`'4'` + 28 字节。json：`'1'` + `.../downlink-ack/server`。

A/B/C 前置：**同一 `device_type`**（该类型 fixture `DownlinkAck=true`），不是同一 ACK mode。三台不同 `device_id`，已 register 且 status=1，TTS ≥2 片。A=binary/0，B=binary/500，C=json/500。

### 4.10 身份

创建后 `device_id` 不可变。PUT/faults 改 ID → 400。skip_register 用夹具 ID 新建。

### 4.11 `fail_connection` 与 Starting 收口

与 stop 共用清理。**先写 `connection_failed` 到 event_log，再唤醒 waiter。**

**触发：** 握手失败；**register_ack_timeout**；**register `code != 0`**；读/写错误；远端关闭；report 发送失败；initial report 超时。

**`register_ack_timeout_sec`（默认 5）：** 进入 Registering（已发出 register）时启动。到期仍无 `/register/client` → `fail_connection(register_timeout)`。基线 Redis `SetNX` 失败可 **不发 ACK**（`register.go`），必须靠本超时收口。  
`skip_register` 不发 register，**不**启动该计时器。

**register 拒绝：** `ack_failure` 写入日志后 **立即** `fail_connection(register_nack)` → Stopped。不得留在 Starting/Registering。

**步骤（幂等）：** Stopping → 取消计时器（含 register/report/完成矩阵）→ 槽按 §4.7 → 清空 pending → **append `connection_failed`** → 唤醒 waiter → 关 WS → Disconnected → **Stopped**（`last_error`）。可再 start。禁止停在 Starting/Running。

`start` 仅 Created 或 Stopped。Starting 期间再次 start → 409。

### 4.12 批量、模板、错峰、限额（Phase 2 必须交付）

| 配置 | 含义 |
|------|------|
| `max_connections` | 同时 Running+Starting 上限；超出 → 429 |
| `max_concurrent_speaking` | 全局非 Terminal Turn 上限；超出的 speak → 409 或 429 |
| `default_stagger_ms` | 批量 start/stop 默认间隔（建议 50） |
| `per_device_buffer_bytes` | 每设备读缓冲上限 |

**模板：** `POST /templates` 或加载 `configs/templates/*.yaml`。批量创建：`template_id` + `count` + `id_prefix`，生成互不冲突、创建后不可变的 `device_id`（如 `{prefix}_{n}`）。冲突 409。

**API：** `POST /devices` 支持 `count`/`template_id`；`POST /devices/batch/start|stop|delete`，body 含 `device_ids` 与 `stagger_ms`。按序错峰，记录每台实际 start 时间。

单设备异常 recover，不拖垮进程。

## 5. 硬约束

收包按字节；头 100B；CAS Reserved；Stage=4 先 vad；report 锁内取号；线上 pcm；ACK 见 §4.9；Starting 见 §4.11；完成见 §4.4。

## 6–7. 音频与选型

内部 PCM → slice pcm 字节。推荐 Go。

## 8. 阶段

| 阶段 | 交付 |
|------|------|
| Phase 1 | protocol、矩阵、缓存提前下行、原子 pending、register 超时、binary ACK、CLI |
| Phase 2 | Manager 批量/stagger/限额、生命周期、wait 锁、JSON ACK、A/B/C |
| Phase 3 | UI |
| Phase 4 | queue、非 pcm、静默探针 |

## 9. 目录

`cmd/speak` `cmd/check` `cmd/fixture` `testdata/fixtures/fresh_ids.jsonl` `configs/templates/`

## 10. 对齐基线

仓库 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`。  
register Redis 出错可不 ACK。管理消息独立 goroutine。MH Seq 例外。不要用 `projects/go/ai-creates-wealth`。
