# 玩具设备模拟器 — 整体架构设计文档（v9）

设备只响应报文上的 ACK 标志，不查询服务端类型配置 `DownlinkAck`。  
创建后的 `device_id` 是实例主键，不可变。  
Turn 完成条件按「相关下行」矩阵，而不是只认 TTS。

## 1. 背景与目标

玩具项目通过 WebSocket 为设备提供 ASR → LLM → TTS。本模拟器用于：

- 模拟真实玩具设备完整协议行为
- 批量设备并发测试
- 参数可配置、可保存
- Agent 可编程 API
- 人工调试界面（配置 + 简单对话）

协议依据：`docs/toy-device-websocket-protocol.md` 与本文 §10 对齐基线。主路径：WebSocket `Action=chatbot`。

## 2. 设计原则

1. **协议忠实优先**：行为对齐协议文档与基线 `types.AudioHeader`。`example/asr/mock.go` 仅作现网脚本缺陷清单，禁止作为 golden 或行为权威。
2. **失败按 fault 矩阵推断**：禁止「凡注入必 drop」。客户端不得假装发出服务端内部日志原因名。
3. **Turn 是一等公民**：拆分 `uplink_end_reason` 与 `turn_end_reason`。**`uplink_end_reason` 先写不改**（含 `vad`）。
4. **Turn 占用原子**：`speak` 请求受理时 CAS 创建 `Reserved`，不是第一帧发出才占用。
5. **API 优先，UI 后置**。生命周期动作有明确前置、原子步骤与响应时机（见 `phase2.md` §5）。waiter 与状态检查同一临界区。
6. **Phase 1/2 线上格式强制 pcm**（raw s16le mono）。WAV 只作源文件。
7. **状态机正交**：Connection / UplinkTurn / DownlinkPlayer。
8. **ACK 运行时只看帧内标志**：音频 `NeedAck==1` 或指令 `need_ack==1` 必须 ACK。服务端 `DownlinkAck` 只出现在集成测试 fixture 说明中。
9. **report 按精确序号匹配**：`pending_reports[sequence]`，禁止单一 latest。仅初始 report 的匹配回显使 Connection 进入 Ready。第一个发出的序号等于 `report_sequence_start`。
10. **实例身份不可变**：`device_id` 在 `POST /devices`（或 Phase 1 进程启动）时确定，之后不得改。
11. **连接失败与主动 stop 走同一套清理**：不得卡在 Starting/Running。

## 3. 总体架构

```text
┌─────────────────────────────────────────────────────────────┐
│ Control Plane                                                │
│  Web UI  │  REST + WS API  │  CLI / Scenario / cmd/fixture   │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Device Manager                                               │
│ 生命周期 / 模板 / 错峰 / 每设备 Turn 槽 CAS / 注入 / 事件总线  │
│ 实例键 = 创建时的 device_id（不可变）                         │
│ fail_connection 与 stop 共用清理                              │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ DeviceInstance                                               │
│ Connection + pending_reports[] + UplinkTurn + DownlinkPlayer │
│ event_log[seq] + waiters                                     │
│ 内部 PCM 时间轴 → pcm 分帧                                   │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ protocol                                                     │
│ AudioHeader / '1' 管理信封 / '4' ACK / '{' 无前缀 JSON       │
└───────────────────────────┬─────────────────────────────────┘
                            │
 Persistence: YAML + recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid
├── state                      # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── injected_fault
├── uplink_end_reason          # 空 | stage2 | interrupt | vad | error | timeout；非空后禁止改写
├── turn_end_reason            # idle | interrupt | error | timeout | connection_lost
├── reply_kind                 # 空 | tts | command | json | silent | 组合（如 command+tts）
├── related                    # 本轮已观察到的相关下行集合
├── first_reply_timer
├── settle_timer               # TTS idle 或非音频 followup
├── uplink_chunks[] / downlink_chunks[]
├── tts_path / format
├── asr_results[]
├── commands[] / json_replies[] / errors[] / events[]
```

**占用规则**

- 每设备一个槽：空闲，或指向唯一非 Terminal Turn。
- `speak` / `speak_and_wait` / 注入发音频：受理时 **CAS** 创建 `Reserved`。`speak_and_wait` 必须在 **同一临界区** 注册 completion waiter。
- CAS 失败：默认 **reject → 409**。
- 禁止以「第一帧是否已发出」判断占用。

**本地 active** = 槽非空。与「服务端是否有 ASR session」不是同一回事。

### 4.2 `uplink_end_reason` 写入规则（先写不改）

| 触发 | 若当前为空 | 若已非空 |
|------|------------|----------|
| 收到 Stage=4 | **立即**记 `vad`，再停发 Stage=1 并补 Stage=2 | 保持原值 |
| 发出 Stage=2 | 记 `stage2` | **保持** |
| 本地发出 Stage=3/5 | 记 `interrupt` | 保持 |
| 取消表 | 仅 Reserved：保持空；其余见 §4.7，非空则仍保持 | 保持 |
| `fail_connection` 且套接字已死 | 记 `error` | 保持 |

`turn_end_reason` 可在取消时设为 `interrupt`，与上行收口独立。连接失败时为 `connection_lost`。

### 4.3 Event

每条事件必有 `device_id`，并写入该实例 **单调递增** 的 `event_seq`（从 1 起）。第二关联键按类型，**禁止**声称所有事件都有 `turn_id`。

| 阶段 / 事件 | 关联键 |
|-------------|--------|
| 握手、Connected、Registering、`ack_failure`、Registered、Reporting、`report_echo`、`report_echo_unmatched`、`report_timeout`、`connection_failed` | **`correlation_id`**（连接级；与是否存在活动 Turn **无关**） |
| Reserved 及之后：speak、asr_result、command_received、json_reply、tts_chunk、tts_done、vad、cancel、expected_server_drop、protocol_error（属本轮） | **`turn_id`**（可另带 `correlation_id`） |

| 事件类型 | 含义 |
|----------|------|
| `local_validation_error` | 正常路径非法，帧未发出 |
| `expected_server_drop` | WaitingReply 首包到期，且 **无** 终态相关下行、**无** 匹配 SessionID 的 `asr_result`（IsFinal 亦可无）。仅矩阵写 drop 的行作为通过条件 |
| `protocol_error` | 无前缀 JSON `Code=1` / `14007`，或非法首字节 |
| `ack_failure` | **仅** `'1'` + `/register/client` 且 `data.code != 0` |
| `report_echo` | `/report/client` 的 `ReportData` 命中 `pending_reports` |
| `report_echo_unmatched` | 回显序号不在表中 |
| `report_timeout` | pending 项超时 |
| `connection_failed` | 走 `fail_connection`；`reason` 为 handshake / read / write / remote_close / report_send |
| `asr_result` | 无前缀 JSON `Action=asr_result` |
| `command_received` | `'1'` + topic 以 `/command/client` 结尾 |
| `json_reply` | 无前缀 JSON `Code==0` 且不是 `asr_result` |
| `tts_done` | 本轮 ≥1 帧匹配 UUID 的 TTS，且 settle 因 TTS idle 结束 |

`inferred_no_reply` 是 `expected_server_drop` 的别名；实现只保留一个主类型。

**禁止**发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志原因名。

### 4.4 相关下行：关联与终止矩阵

基线轮次不一定下发 TTS：

- `turn_stage_command.go`：`voiceSyncCommands` → `SyncDevice`（`'1'` `/command/client`）后结束
- `ReplyModeText` + WebSocket → `SendWebsocketJSON`（无前缀，`Code=0`，`Data.Type=chat_reply`）
- `replyText==""` → 直接结束，**设备可能收不到任何终态下行**

因此 **禁止**「只有匹配 UUID 的 TTS 才能取消首包计时 / 结束 Turn」。

#### 4.4.1 关联规则

| 种类 | 线上形态 | 如何判为本轮 |
|------|----------|--------------|
| TTS / 提示音中与本轮同 UUID 者 | `'0'` + 头 | `AudioHeader.UUID == uplink_uuid`。其它 UUID **不计入**本轮完成（可另存） |
| command | `'1'` + `/command/client` | 指令 **无音频 UUID**。本设备槽内 Turn 处于 Speaking（已发过 `'0'`）/ FinishingUpload / WaitingReply 时归入该 Turn |
| asr_result | `{` `Action=asr_result` | `SessionID` 等于 `uplink_uuid` 的十进制字符串 |
| 成功 JSON | `{` `Code==0`（基线 `HTTPSuccess=0`），且不是 asr_result | 同 command 的占用关联。常见 `Data.Type=chat_reply` 或流式 `chat_reply_chunk` / `chat_reply_end` |
| 失败 JSON | `{` `Code==1` 或 `14007` | 占用关联；记 `protocol_error`，Turn `error` |

`asr_result` 是 **进度**，单独不构成终态下行。

#### 4.4.2 计时

进入 **WaitingReply** 时启动 `first_reply_timer` = `first_reply_timeout_sec`（默认 20）。此时 **不** 启动 settle。

| 计时器 | 启动 | 取消 / 重置 |
|--------|------|-------------|
| `first_reply_timer` | 进入 WaitingReply | 收到第一条 **终态相关下行**（匹配 TTS / command / 成功 JSON / 失败 JSON） |
| `settle_timer`（TTS idle） | 匹配 UUID 的 TTS 分片 | 后续匹配 TTS **重置** 为 `downlink_idle_timeout_sec`（默认 20） |
| `settle_timer`（非音频 followup） | command 或成功 JSON，且本轮尚无匹配 TTS | 后续同类终态下行重置为 `non_audio_followup_sec`（默认 5）。若随后出现匹配 TTS，**改写** 为 TTS idle 规则 |
| `post_final_asr_silence` | 仅当 first_reply 到期且已有匹配 `asr_result`（建议 IsFinal）且仍无终态下行 | 到期 → silent 成功（见矩阵） |

`--wait` / `speak_and_wait` 等的是 **Turn Terminal**。矩阵保证负向 drop 在 `first_reply_timeout_sec` 内结束。

#### 4.4.3 终止矩阵

| 本轮观察到的相关下行 | `reply_kind` | 何时 Terminal | `turn_end_reason` | 完成事件 |
|----------------------|--------------|---------------|-------------------|----------|
| 仅匹配 TTS | `tts` | TTS idle 到期 | `idle` | `tts_done` |
| 仅 command | `command` | followup 到期且期间无匹配 TTS | `idle` | `command_received`（无 `tts_done`） |
| 仅成功 JSON | `json` | 同上 | `idle` | `json_reply` |
| command + 匹配 TTS | `command+tts` | 按 TTS idle（TTS 到达后切换） | `idle` | `command_received` + `tts_done` |
| 成功 JSON + 匹配 TTS | `json+tts` | 按 TTS idle | `idle` | `json_reply` + `tts_done` |
| 仅匹配 asr_result，无终态下行 | `silent` | first_reply 到期后走 `post_final_asr_silence`（默认 5s，可与 followup 共用配置）再结束 | `idle` | 有 `asr_result`，无 `tts_done` |
| 无相关下行（无 asr、无终态） | 空 | `first_reply_timer` 到期 | `timeout`；矩阵 drop 行另发 `expected_server_drop` | 不得标 `tts_done` |
| 失败 JSON | 空或已有集合 | 立即 | `error` | `protocol_error` |
| 取消表 | 保持已有 related | 立即 | `interrupt` | 取消计时器 |
| `fail_connection` | 保持已有 | 立即 | `connection_lost` | `connection_failed` |

**设备侧不可区分的静默成功：** `replyText==""`、未 `SyncDevice`、非 text 模式、且未开 `StreamingAsrTextReply` 时，服务端可结束轮次而设备收不到任何相关包。Phase 1/2 **按「无相关下行」行处理**（正常路径 timeout；drop 行 expected_server_drop）。此类技能不要用默认「等 idle 成功」Scenario。服务端探针放到 Phase 4。

**`tts_done` 定义不变：** ≥1 帧匹配 UUID 的 TTS，且本次 Terminal 经由 TTS idle。零 TTS 的 command/json/silent **不得**标 `tts_done`。

#### 4.4.4 等待超时预算

禁止把 HTTP/CLI/Scenario 默认超时写死为 30s。

```text
wait_timeout_sec >= upload_sec
                  + first_reply_timeout_sec
                  + max(downlink_idle_timeout_sec, non_audio_followup_sec)
                  + wait_timeout_slack_sec
```

- `upload_sec` = 本轮内部 PCM 时长（含 silence）
- `wait_timeout_slack_sec` 默认 5
- 请求省略 `timeout_sec` 时，服务端按上式计算（音频未知则 `upload_sec=0`，并在文档/响应里写出所用预算）
- 示例：3s 音频 + 20 + 20 + 5 = **48s**。Scenario 应省略 timeout 或填 ≥ 预算的值

### 4.5 Scenario

默认成功断言随 **预期回复类型**，禁止一律 `tts_done`：

- 对话 TTS：`tts_done` + `turn_end_reason=idle`
- 纯指令：`command_received` + `idle`，且无 `tts_done` 要求
- 文本模式：`json_reply` + `idle`
- `asr_contains` 仅当 `StreamingAsrTextReply=true`，标 optional
- 注入按 §4.6（`skip_report` 不得 assert drop）

### 4.6 注入 fault 矩阵

基线：`LookupCachedDevicePlayMode` 先内存缓存再 Mongo `status=1`，**不要求本连接执行过 register**。`ParseAudioHeader` 只拒绝剥掉 `'0'` 后 `len < 100`。

模拟器 **没有** Mongo / core 查询通道，**禁止** CLI/API 自行判断设备是否 `status=1`。

**`skip_register` 夹具：**

1. 隔离 core（空表或仅本套件前缀）。
2. `device_id = sim_sr_{run_uuid}_{n}`。
3. 本趟 **禁止**对该 ID register。
4. 写入 `testdata/fixtures/fresh_ids.jsonl`：`{id, run_uuid, isolation, generated_at}`。
5. 验收核对 jsonl，不能只看退出码。
6. Manager：用夹具 ID **新建**实例，禁止改已有 `device_id`。

| fault | 前置 | 精确出站 | 期望 |
|-------|------|----------|------|
| `skip_register` | 夹具新鲜 ID；新建实例 | 不 register；合法音频 + Stage=2 | `expected_server_drop`（无相关下行） |
| `skip_report` | 可已 register | 不 report；合法音频 | **不默认 drop**；允许 TTS/command/JSON |
| `bad_seq` | 非 MH；CAS Reserved 后发；服务端无 ASR session | Stage=1 Seq>=1，头 100B | `expected_server_drop` |
| `oversize` | 注入 | payload>51200 | `expected_server_drop` |
| `bad_header` | 注入 | `'0'`+<100B | `expected_server_drop` |
| `dup_uuid` / `bad_stage` | — | 可录帧 | **不作为 drop 验收** |

### 4.7 取消转移表

`cancel_previous` / `interrupt` / `stop` / `fail_connection`（套接字仍可写时）按表执行。  
`uplink_end_reason` 已非空则本表不得改写它。

| 当前状态 | 出站（套接字已死则跳过发送） | 空的 uplink_end_reason | turn_end_reason |
|----------|------------------------------|------------------------|-----------------|
| Reserved | 不发 Stage=3 | 保持空 | interrupt；连接失败则为 connection_lost |
| Speaking | Stage=3；停 Stage=1 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；可发 Stage=3 | interrupt / error | 同上 |
| FinishingUpload，Stage=2 已发 | 可发 Stage=3 | 保持已有 | 同上 |
| WaitingReply | 可发 Stage=3；取消全部计时器 | 保持已有 | 同上 |
| Terminal | 无 | 不变 | 不变 |

`cancel_previous` 之后：新 UUID、Seq=0、新 Reserved。  
`stop` / `fail_connection`：不创建新 Turn。

### 4.8 report 在途序号

基线管理消息独立 goroutine，回显可乱序。

```text
report_next_seq     # 初值 = report_sequence_start（默认 1）
pending_reports     # map[sequence_number] → {kind: initial|keepalive|manual, correlation_id, timeout}
```

**第一个发出的 `sequence_number` 必须等于 `report_sequence_start`。**  
禁止：计数器初值设为 start 再 `atomic.AddUint64(&x, 1)` 作为首个序号（得到 start+1）。  
若用 `atomic.AddUint64`：内部初值设为 `start-1`（要求 `start>=1`）。推荐显式「读取当前值、再 +1 写回」。

发送：`seq = report_next_seq`；`report_next_seq += 1`；登记 pending；再发。禁止 latest。

接收：按 `data.sequence_number` 精确查找。仅 `kind==initial` 且 Connection==Reporting → Ready。未命中 → `report_echo_unmatched`。到期 → `report_timeout`；initial 仍 Reporting 则连接失败（§4.11），不是 `ack_failure`。

keepalive 仅 Ready 之后。`POST /report` 仅 Ready，否则 409。

### 4.9 ACK

**运行时：** `NeedAck==1` 或指令 `need_ack==1` 必须按 `mode` ACK；为 0 不发。禁止查 `DownlinkAck`。

| 阶段 | 必须实现 | 不交付 |
|------|----------|--------|
| Phase 1 | 音频与指令 **binary** ACK；`sleep_ms=0` | JSON ACK；非零 SleepMs；节流对比 |
| Phase 2 | `mode=json`；`sleep_ms>0`；A/B/C | — |

fixture：需要标志时 core 类型 `DownlinkAck=true`。`expect_downlink_need_ack` 不是运行时开关。

`sleep_ms: 0` 不写入节流缓存，不能清旧值。正值用例必须不同 `device_id`。

#### 音频 ACK

| binary | 取值 |
|--------|------|
| Ack | 音频 Seq |
| DownlinkType | TTS=1；提示音=2 |
| Code / SleepMs | 配置 |
| 内存字段 | 0 |

JSON（Phase 2）：`ack`/`sequence_number`=音频 Seq；`downlink_type`=`tts`/`hint_audio`；`uuid`=音频 UUID；`sleep_ms`/`code`=配置。

#### 指令 ACK

| binary | 取值 |
|--------|------|
| Ack | 指令 `sequence_number`（缺省 0） |
| DownlinkType | **3** |
| Code / SleepMs | 配置 |

JSON（Phase 2）：`downlink_type=command`；`topic`=原指令完整 topic；`uuid` 省略或 0。

`mode=binary`：`'4'` + 28 字节。`mode=json`：`'1'` + `.../downlink-ack/server`。

### 4.10 实例身份

- Manager 键 = 创建时 `device_id`：路由、事件、槽、`recordings/{device_id}/`。
- `POST /devices` 冲突 → 409；成功后不可变。
- `PUT /config`：body `device_id` 与路径不一致 → 400。
- `POST /faults` 不得改 ID。
- `skip_register`：夹具 ID **新建**实例。
- Phase 1 CLI `--device-id` 只是进程启动身份。

### 4.11 连接失败收口（`fail_connection`）

与主动 `stop` **共用清理步骤**，区别是原因与是否尝试发 Stage=3。

**触发：** 握手失败；读错误；写错误；远端关闭；register/report/音频帧发送失败。

**步骤（幂等；已 Stopping/Stopped/Deleted 则忽略）：**

1. 实例 → Stopping。
2. 取消全部计时器。
3. 槽非空：按 §4.7；套接字已死则 **跳过出站**，仍 Terminal。
4. 清空 `pending_reports`（各项 `report_timeout` 或 cancelled）。
5. 唤醒全部 waiter（含 `speak_and_wait` / `wait`）。
6. 关闭本地 WS（若仍开）。
7. Connection → Disconnected。
8. 实例 → **Stopped**（写入 `last_error`）。**允许再次 `start`。**
9. 事件 `connection_failed`。

禁止停留在 Starting 或 Running。`start` 仅 Accepted：Created 或 Stopped。

## 5. 协议硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text 帧，不得 UTF-8 解码下行 |
| AudioHeader | 100 字节；golden 对照基线；禁止 mock.go |
| 首字节 | `'1'` / `'0'` / `'4'` / `'{'` |
| 占用 | 受理时 CAS Reserved |
| 新一轮 | Seq=0 + Stage=1；禁止 MH 前缀机型 |
| Stage=4 | 立即记 `vad`（若空），补 Stage=2 |
| 下行结束 | 无稳定 Stage=2；完成条件见 §4.4 |
| 心跳 | 周期 report；仅 Ready 后 |
| Register | `/register/client` `code==0` |
| Report | §4.8；首个序号 = `report_sequence_start` |
| 线上格式 | pcm s16le mono |
| ACK | §4.9 |
| 身份 | §4.10 |
| 连接失败 | §4.11 |

## 6. 音频管线

```text
源 WAV 解 RIFF → 内部 PCM（mono s16le）
  → silence = 全零采样
  → Phase 1/2：按 slice_ms 切 pcm 字节
  → Phase 4 非 pcm：整段只编码一次再切流
```

禁止拼接带 RIFF 的文件字节。

## 7. 技术选型

硬门槛：按字节收 TextMessage 中的二进制音频。Phase 1 **推荐 Go**。真实 core 收到至少一帧 `'0'` TTS 且未因 UTF-8 失败后锁定。

## 8. 分阶段总览

| 阶段 | 交付 |
|------|------|
| Phase 1 | protocol、Reserved、矩阵、pcm、`pending_reports`、§4.4 完成条件、`fail_connection` 状态机、CLI、音频+指令 binary ACK |
| Phase 2 | Manager、REST/WS 生命周期；wait 锁内检查+游标；JSON ACK 与节流；`device_id` 不可变 |
| Phase 3 | Web UI |
| Phase 4 | queue；非 pcm；静默成功探针；可选 Redis/Mongo |

## 9. 推荐目录结构

```text
toy-device-simulator/
├── protocol/ core/ manager/ api/ scenario/
├── cmd/speak/  cmd/check/  cmd/fixture/
├── configs/example_device.yaml
├── testdata/golden_frames/  testdata/fixtures/fresh_ids.jsonl  testdata/audio/
└── recordings/{device_id}/{turn_id}/
```

## 10. 对齐基线

| 项 | 值 |
|----|----|
| 仓库 | `C:\Users\xie_f\projects\other\ai-creates-wealth` |
| commit | `5a02d70cdf964bdafea7be92495ad1d0a63499c5` |
| MH Seq 例外 | `strings.HasPrefix(deviceType, "MH")` |
| 非 MH 无 ASR session + Seq>0 | 丢弃 |
| 纯指令结束 | `SyncDevice` → `/command/client` |
| 文本回复 | `ReplyModeText` → 无前缀 JSON `Code=0` |
| 空回复 | `replyText==""` 可无设备下行 |
| asr_result | `StreamingAsrTextReply`；`SessionID` 为 UUID 十进制 |
| report 回显 | 可乱序 |
| ACK 置位 | 服务端写报文标志；设备只读报文 |
| SleepMs=0 | `UpdateMemoryState` 跳过写入 |
| 节流封顶 | 默认 6000ms |
| 本地 core | `ws://127.0.0.1:8089/` |

不要把 `C:\Users\xie_f\projects\go\ai-creates-wealth` 当作权威（Seq 分叉）。
