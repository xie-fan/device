# 玩具设备模拟器 — 整体架构设计文档（v8）

设备只响应报文上的 ACK 标志，不查询服务端类型配置 `DownlinkAck`。  
创建后的 `device_id` 是实例主键，不可变。

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
5. **API 优先，UI 后置**。生命周期动作有明确前置、原子步骤与响应时机（见 `phase2.md` §5）。
6. **Phase 1/2 线上格式强制 pcm**（raw s16le mono）。WAV 只作源文件。
7. **状态机正交**：Connection / UplinkTurn / DownlinkPlayer。
8. **ACK 运行时只看帧内标志**：音频 `NeedAck==1` 或指令 `need_ack==1` 必须 ACK。服务端 `DownlinkAck` 只出现在集成测试 fixture 说明中。
9. **report 按精确序号匹配**：`pending_reports[sequence]`，禁止单一 latest。仅初始 report 的匹配回显使 Connection 进入 Ready。
10. **实例身份不可变**：`device_id` 在 `POST /devices`（或 Phase 1 进程启动）时确定，之后不得改。

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
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ DeviceInstance                                               │
│ Connection + pending_reports[] + UplinkTurn + DownlinkPlayer │
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
├── turn_id / uplink_uuid      # Reserved 时分配，发帧前已存在
├── state                      # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── injected_fault             # 无则走正常路径
├── uplink_end_reason          # 空 | stage2 | interrupt | vad | error | timeout
│                              # 非空后禁止改写（含随后的 Stage=2 / 取消）
├── turn_end_reason            # idle | interrupt | error | timeout；赋值即 Terminal，释放槽
├── first_reply_timer          # 进入 WaitingReply 时启动；首帧匹配后取消
├── downlink_idle_timer        # 首帧匹配后启动；后续匹配帧重置
├── uplink_chunks[] / downlink_chunks[]
├── tts_path / format
├── asr_results[]              # 仅当设备类型开启 StreamingAsrTextReply
├── commands[] / errors[] / events[]
```

**占用规则**

- 每设备一个槽：空闲，或指向唯一非 Terminal Turn。
- `speak` / `speak_and_wait` / 注入发音频：受理时 **CAS** 创建 `Reserved`（分配 `turn_id`、`uplink_uuid`）。成功才开始发帧。
- CAS 失败：默认策略 **reject → 409**。槽占用包括 Reserved、Speaking、FinishingUpload、WaitingReply（下行 PlayingTTS 时 UplinkTurn 为 WaitingReply）。
- 禁止以「第一帧是否已发出」判断占用。两个并发 speak 必须一个 Reserved、一个 409。

**本地 active** = 槽非空（Reserved 起到 Terminal 之前）。  
这与「服务端是否有 active ASR session」不是同一回事。

### 4.2 `uplink_end_reason` 写入规则（先写不改）

| 触发 | 若当前为空 | 若已非空 |
|------|------------|----------|
| 收到 Stage=4 | **立即**记 `vad`，再停发 Stage=1 并补 Stage=2 | 保持原值 |
| 发出 Stage=2 | 记 `stage2` | **保持**（VAD 后补 Stage=2 仍是 `vad`） |
| 本地发出 Stage=3/5 | 记 `interrupt` | 保持 |
| 取消 | 仅 Reserved：保持空；其余见 §4.6，但非空则仍保持 | 保持 |

`turn_end_reason` 可在取消时设为 `interrupt`，与上行收口独立。

### 4.3 Event

每条事件必有 `device_id`。第二关联键按类型，**禁止**声称所有事件都有 `turn_id`。

| 阶段 / 事件 | 关联键 |
|-------------|--------|
| 握手、Connected、Registering、`ack_failure`、Registered、Reporting、`report_echo`、`report_echo_unmatched`、`report_timeout` | **`correlation_id`**（连接级；与是否存在活动 Turn **无关**。keepalive / 手动 report 可发生在 Turn 期间，仍用连接级 ID，不改用 `turn_id`） |
| Reserved 及之后：speak、tts_chunk、tts_done、vad、cancel、expected_server_drop、protocol_error（属本轮） | **`turn_id`**（可另带 `correlation_id`） |

| 事件类型 | 含义 |
|----------|------|
| `local_validation_error` | 正常路径非法，帧未发出 |
| `expected_server_drop` | 已进入 WaitingReply，**首包等待**到期且无匹配 UUID 的 ASR/TTS。仅当矩阵该行写 drop 时作为通过条件 |
| `protocol_error` | 无前缀 JSON `Code=1` / `14007`，或非法首字节 |
| `ack_failure` | **仅** `'1'` + topic 以 `/register/client` 结尾且 `data.code != 0`（含 5001） |
| `report_echo` | `'1'` + topic 以 `/report/client` 结尾，`data` 为 `ReportData` 回显（不是 AckResponse），且 `data.sequence_number` **命中** `pending_reports` 中对应项 |
| `report_echo_unmatched` | 收到 report 回显但序号不在 `pending_reports`（重复、过期或从未发送） |
| `report_timeout` | 某 `pending_reports` 项在 `report_echo_timeout_sec` 内未匹配 |

`inferred_no_reply` 是 `expected_server_drop` 的别名；实现枚举只保留一个主类型。

**禁止**发出 `device_not_found`、`status_invalid`、`no_active_turn` 等服务端日志原因名。

### 4.4 下行完成与等待计时（`--wait` 不得悬挂）

Turn 进入 **WaitingReply**（Stage=2 已发出，或注入路径按矩阵发出末帧并转入等待）时：

1. 启动 **`first_reply_timer`** = `first_reply_timeout_sec`（默认 20）。
2. **尚未**启动下行 idle 计时。

收到 **第一帧** 匹配 `uplink_uuid` 的 `'0'` Stage=1：

- 取消 `first_reply_timer`
- 记 `tts_chunk`；DownlinkPlayer → PlayingTTS
- 启动 **`downlink_idle_timer`** = `downlink_idle_timeout_sec`（默认 20）

之后每帧匹配的 TTS：**重置** `downlink_idle_timer`，不重启首包计时。

| 计时到期 | 条件 | 结果 |
|----------|------|------|
| `first_reply_timer` | 零帧匹配 TTS | `turn_end_reason=timeout`；矩阵该行期望 drop 则发 `expected_server_drop`；Terminal；释放槽；唤醒全部 waiter |
| `downlink_idle_timer` | 已有 ≥1 帧匹配 TTS | `tts_done`；`turn_end_reason=idle`；Terminal；释放槽；唤醒 waiter |
| 取消表执行 | 任一时钟运行中 | 取消两个计时器；按 §4.6 Terminal |

`--wait` / `speak_and_wait` / `POST /wait` 等待的是 **Turn Terminal**（或指定事件），上表保证负向用例在 `first_reply_timeout_sec` 内结束，不会无限挂起。

`tts_done`：本轮至少一帧 `tts_chunk`，且 DownlinkPlayer 因 idle 回到 Idle。零下行不得标 `tts_done`。

### 4.5 Scenario

主断言：`tts_done` + `turn_end_reason=idle`。  
`asr_contains` 仅当设备类型 `StreamingAsrTextReply=true`，标为 optional。  
注入类 Scenario 按 §4.6 写断言（例如 `skip_report` 不得 assert drop）。

### 4.6 注入 fault 矩阵

基线：`LookupCachedDevicePlayMode` 先内存缓存再 Mongo `status=1`，**不要求本连接执行过 register**。`ParseAudioHeader` 只拒绝剥掉 `'0'` 后 `len < 100`。

模拟器 **没有** Mongo / core 查询通道，**禁止** CLI/API 自行判断设备是否 `status=1`。

**`skip_register` 的「未注册」由测试夹具证明：**

1. 使用隔离 core（空 device 表，或约定前缀仅本套件写入）。
2. 生成 `device_id = sim_sr_{run_uuid}_{n}`，`run_uuid` 为本趟 UUIDv4。
3. 本趟 setup **禁止**对该 ID 执行 register。
4. 写入 `testdata/fixtures/fresh_ids.jsonl`：`{id, run_uuid, isolation, generated_at}`。
5. 验收必须核对 jsonl 记录以及套件未预注册；不能只断言 speak 进程退出码为 0。
6. **Manager 路径**：用该夹具 ID **新建**实例（`POST /devices`），禁止改已有实例的 `device_id`。

误用已存在 ID 做 `skip_register`：测试无效，由夹具/Scenario setup 负责。

| fault | 前置 | 精确出站 | 期望 |
|-------|------|----------|------|
| `skip_register` | 上列夹具新鲜 ID；新建实例 | 握手后不发 register；合法 `'0'` 头 100 字节 + pcm + Stage=2 | `expected_server_drop`；outbound 有音频帧 |
| `skip_report` | 允许已存在或刚 register 成功的设备 | 发 register 并等到 `code==0`；**不发 report**；发合法音频 | **不默认 drop**。允许 ASR/TTS。Connection 停在 Registered |
| `bad_seq` | 非 MH 机型；**请求前本地槽空，CAS 成 Reserved 后再发**；**发帧时服务端无 active ASR session**（本轮未发 Seq=0）。本地发帧时槽已非空，这是预期 | `'0'` + 合法 100 字节头，`Stage=1`，`Seq>=1`，payload 合法 | `expected_server_drop` |
| `oversize` | 注入路径 | `'0'` + 合法 100 字节头 + **payload > 51200** | `expected_server_drop` |
| `bad_header` | 注入路径 | `'0'` + 其后 **少于 100 字节**（推荐 10 字节 `0x00`）。禁止用「padding/magic 错但总长仍 100」 | `expected_server_drop` |
| `dup_uuid` / `bad_stage` | — | 可发送、可录帧 | **不作为 drop 验收** |

### 4.7 取消转移表（interrupt / cancel_previous / stop）

新 speak 在 CAS 前若策略为 `cancel_previous`：对当前槽内 Turn 按行执行，待其 Terminal 并释放槽后，再 CAS 新 Reserved。  
`POST .../stop` 与 `DELETE` 若槽非空，同样执行本表，然后关连接。

执行前：若 `uplink_end_reason` 已非空，**本表不得改写它**（只改 `turn_end_reason` 与出站）。

| 当前状态 | 出站 | 空的 uplink_end_reason 变为 | turn_end_reason |
|----------|------|------------------------------|-----------------|
| Reserved（尚未发任何 `'0'`） | 不发 Stage=3 | **保持空（不赋值）** | `interrupt`，立即 Terminal |
| Speaking | 发 Stage=3；停后续 Stage=1 | `interrupt` | `interrupt` |
| FinishingUpload，Stage=2 **未**发出 | 不发 Stage=2；发 Stage=3 | `interrupt`（若已因 VAD 为 `vad` 则保持 `vad`） | `interrupt` |
| FinishingUpload，Stage=2 **已**发出 | 发 Stage=3 停 TTS | 保持已有（`vad` 或 `stage2`） | `interrupt` |
| WaitingReply / 下行 PlayingTTS | 发 Stage=3；取消两个下行计时器 | 保持已有 | `interrupt` |
| Terminal | 无操作 | 不变 | 不变 |

然后（仅 `cancel_previous` 的新 speak）：新 UUID、Seq=0、新 Reserved。禁止同一 UUID 交叉写帧。  
`stop` / `delete`：不创建新 Turn，释放槽后关 WS。

### 4.8 report 在途序号（`pending_reports`）

基线：`handle.go` 解析管理 JSON 后对每条消息 `go func()` 分发；`Report` 把原始 `ReportData` 回显到 `.../report/client`。多条 report 的回显 **可以乱序**。

每个 DeviceInstance：

```text
report_seq          # 原子递增器；初值 report_sequence_start（默认 1）
pending_reports     # map[sequence_number] → ReportWaiter
                    #   kind: initial | keepalive | manual
                    #   correlation_id
                    #   sent_at
                    #   timeout: report_echo_timeout_sec（默认 5）
```

**发送**

1. `seq = atomic_add(report_seq)`（先取号再发）。
2. `pending_reports[seq] = waiter`（`kind` 按本次来源）。
3. 上行 `sequence_number = seq`。
4. **禁止**用单一 `latest_seq` 覆盖未完成项。

**接收 `/report/client`**

1. 校验 topic 五段且以 `/report/client` 结尾；`data` 为 `ReportData`。
2. 用 `data.sequence_number` **精确查找** `pending_reports`。
3. 命中：发 `report_echo`（使用该 waiter 的 `correlation_id`）；删除该项；完成 waiter。
4. **仅当** `kind==initial` **且** Connection 处于 `Reporting`：转入 **Ready**。keepalive / manual 的回显 **不得**把未 Ready 的连接标 Ready，也不得把已 Ready 的连接打回 Reporting。
5. 未命中：发 `report_echo_unmatched`，不改变 Connection。

**超时**

- waiter 到期：删除该项；发 `report_timeout`。
- `kind==initial` 且仍为 Reporting：Connection 失败（不得 speak 正常路径）；不是 `ack_failure`。

**约束**

- keepalive **仅 Ready 之后**启动。
- `POST /report` 仅 Running 且 Connection==Ready；否则 409。
- 乱序合法：seq=6 的回显先于 seq=5 到达时，先完成 6 的 waiter，5 仍等待；二者 `correlation_id` 不得交叉。

### 4.9 ACK：运行时 vs 测试 fixture；阶段交付

**运行时（两个阶段代码路径相同，只看报文）：**

- 音频头 `NeedAck == 1` → 必须按当前 `mode` 发 ACK。
- 指令 JSON `need_ack == 1`（topic 以 `/command/client` 结尾）→ 必须发 ACK。
- 标志为 0 → 不发 ACK。
- **禁止**查询、缓存或依赖服务端 `deviceType.DownlinkAck`。

**阶段交付（能力增量，不是运行时条件增量）：**

| 阶段 | 必须实现 | 本阶段不测 / 不交付 |
|------|----------|---------------------|
| Phase 1 | 音频 **与指令** 的 **binary** ACK；`sleep_ms=0`、`code=0` | JSON ACK；非零 SleepMs；节流间隔对比 |
| Phase 2 | `mode=json`；`sleep_ms>0`；音频节流 A/B/C | — |

**集成测试 fixture（套件/种子数据，不是模拟器查询）：**

- 需要报文出现 ACK 标志的用例：core 中该 `device_type` 须 `DownlinkAck=true`，否则服务端不会置位，设备正确选择不 ACK。此时应判 **fixture 问题**。
- YAML `expect_downlink_need_ack` 仅注释，**不是**运行时开关。

服务端备忘：`NeedAckEnabled` 读类型配置，再 `MarkAudioHeaderNeedAck` / `MarkManagePayloadNeedAck` 写入报文。设备只读报文。

`sleep_ms: 0`：出站 ACK 的 SleepMs 与 MemoryPercent 均为 0 时，服务端 `UpdateMemoryState` **跳过写入**，不能清除该设备已有的正值节流状态（TTL 默认约 60s）。正值节流用例必须换新 `device_id`。

#### 音频 ACK 字段

对刚收到的下行 **音频** 帧：

| binary 字段 | 取值 |
|-------------|------|
| Ack | 该帧 AudioHeader.SequenceNumber |
| DownlinkType | TTS=1；提示音=2 |
| Code | 配置 `code`（默认 0） |
| SleepMs | 配置 `sleep_ms` |
| MemoryPercent / MemoryTotalKB / MemoryFreeKB | 0 |
| UUID | 不在 28 字节结构内 |

JSON（Phase 2）：

| 字段 | 取值 |
|------|------|
| ack | 音频 Seq |
| downlink_type | `tts` 或 `hint_audio` |
| uuid | 音频头 UUID |
| sequence_number | 音频 Seq |
| sleep_ms / code | 配置 |
| topic | 可选；音频无管理 topic 时可省略 |

#### 指令 ACK 字段（`need_ack==1`）

对刚收到的 `'1'` 指令：

| binary 字段 | 取值 |
|-------------|------|
| Ack | `data.sequence_number`（缺省 0） |
| DownlinkType | **3**（command） |
| Code / SleepMs | 配置 |
| 其余 | 0 |

JSON（Phase 2）：

| 字段 | 取值 |
|------|------|
| ack | 指令 `sequence_number` |
| downlink_type | `command` |
| sequence_number | 指令 `sequence_number` |
| topic | **原指令完整 topic**（`.../command/client`） |
| uuid | **省略或 0** |
| sleep_ms / code | 配置 |

`mode=binary`：发 `'4'` + 28 字节小端。  
`mode=json`：发 `'1'` + topic `{enterprise}/{deviceType}/{deviceID}/downlink-ack/server`。

### 4.10 实例身份（`device_id` 不可变）

- Manager 以创建时的 `device_id` 为键：路由、事件过滤、Turn 槽、`recordings/{device_id}/`。
- `POST /devices`：若该 ID 已存在 → **409**。成功后该字段 **永久不可变**。
- `PUT /config`：body 中的 `device_id` 若出现且与路径不一致 → **400**；一致则忽略。禁止重键。
- `POST /faults`：**不得**携带或覆盖 `device_id`。
- `skip_register`：先 `alloc_fresh_id`，再 **`POST /devices` 使用该 ID 新建**，再设 fault、start。禁止把已有实例改成夹具 ID。
- Phase 1 CLI `--device-id`：进程启动时的身份，不是运行中重键。

## 5. 协议硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节处理 Text 帧，不得对下行做 UTF-8 文本解码 |
| AudioHeader | 100 字节（含 2 字节 padding），小端；golden 对照对齐基线 `types.AudioHeader`；禁止 mock.go |
| 首字节 | 管理 `'1'`（0x31）；音频 `'0'`（0x30）；ACK `'4'`；无前缀 JSON `'{'` |
| 占用 | 受理时 CAS Reserved |
| 新一轮 | 正常路径 Seq=0 + Stage=1；示例与负向用例禁止 MH 前缀机型 |
| Stage=4 | 立即记 `vad`（若空），停发 Stage=1，补 Stage=2 |
| 下行结束 | 无稳定 Stage=2；首包计时见 §4.4；idle 默认 20s |
| 心跳 | 周期 **report**（不要用 register 保活）；仅 Ready 后 |
| Register | `/register/client` 且 `data.code==0` 才算 Registered；`5001` → `ack_failure` |
| Report | 按 §4.8 精确序号；仅 initial 匹配回显 → Ready |
| 线上格式 Phase 1/2 | **pcm**（raw s16le mono）；配置写成 wav 作为线上格式则启动拒绝 |
| 编码顺序 | 内部 PCM 时间轴 → 按 slice_ms 切 **pcm 字节** 成帧。禁止对每一片单独封装 WAV |
| ACK | 见 §4.9 |
| 身份 | 见 §4.10 |

## 6. 音频管线

```text
源文件（若为 WAV：解析 RIFF，取出 PCM）
  → 转到内部 PCM：mono、signed 16-bit little-endian、sample_rate
  → 拼接 / 插入全零 PCM silence（不是 sleep，不是拼接 WAV 文件）
  → Phase 1/2：按 slice_ms 把 PCM 字节切成帧 payload，AudioFormat=pcm
  → Phase 4 若允许 mp3/wav 上线：对整段 PCM 只跑一次编码器，再切编码流
```

禁止把多个 WAV 文件当字节拼接（会重复 RIFF 头）。  
线上已编码格式若无法生成合法静音帧：该 format 的 silence 在配置阶段拒绝（Phase 4）。

## 7. 技术选型

硬门槛：能按字节接收 TextMessage 中的二进制音频。

Phase 1 Core **推荐 Go**（gorilla/websocket 或同等能力）。对真实 core 收到至少一帧 `'0'` TTS 且未因 UTF-8 失败后，锁定为默认实现。

若用 Python：必须锁定库与版本、返回 raw bytes，并在真实 TTS 帧上通过非法 UTF-8 测试后才能作为选型。

## 8. 分阶段总览

| 阶段 | 交付 |
|------|------|
| Phase 1 | protocol 包、Reserved 状态机、fault 矩阵、pcm 分帧、`pending_reports`、首包/idle 计时、`cmd/speak` `cmd/check` `cmd/fixture`、音频+指令 **binary ACK**（SleepMs=0） |
| Phase 2 | Device Manager、REST+WS 生命周期表；**默认 reject，可选 cancel_previous，不交付 queue**；JSON ACK、非零 SleepMs、节流 A/B/C；查询/配置 API；Scenario；`device_id` 不可变 |
| Phase 3 | Web UI，只消费 Phase 2 API |
| Phase 4 | queue；线上非 pcm；可选 Mongo 预检；可选清 Redis 节流键 |

## 9. 推荐目录结构

```text
toy-device-simulator/
├── protocol/
├── core/
├── manager/
├── api/
├── scenario/
├── cmd/
│   ├── speak/
│   ├── check/
│   └── fixture/          # alloc-fresh-id
├── configs/
│   └── example_device.yaml
├── testdata/
│   ├── golden_frames/    # 由对齐基线 AudioHeader 生成
│   ├── fixtures/fresh_ids.jsonl
│   └── audio/
└── recordings/{device_id}/{turn_id}/
```

## 10. 对齐基线

| 项 | 值 |
|----|----|
| 目标仓库 | `C:\Users\xie_f\projects\other\ai-creates-wealth` |
| commit | `5a02d70cdf964bdafea7be92495ad1d0a63499c5` |
| MH 前缀 Seq 例外 | 是。`strings.HasPrefix(deviceType, "MH")` |
| 非 MH + 服务端无 ASR session + Stage=1 + Seq>0 | 丢弃 |
| 音频准入 | `LookupCachedDevicePlayMode`：内存缓存或 DB `status=1` |
| 头解析 | `ParseAudioHeader`：`len < 100` 失败；不校验 magic/padding |
| report 下行 | `ReportData` 回显，topic `.../report/client`；管理消息独立 goroutine，回显可乱序 |
| ACK 置位 | 服务端按类型 `DownlinkAck` 写入报文 `NeedAck`/`need_ack`；设备只读报文 |
| ACK 节流缓存 | `UpdateMemoryState`：SleepMs 与 MemoryPercent 皆 ≤0 则 **跳过写入** |
| 节流 | `SleepMs>0` 时 `ThrottleBeforeDownlink` 在后续下发前 sleep；`speedCtrl` 默认开；封顶默认 6000ms |
| 本地 core | `ws://127.0.0.1:8089/` |

不要把 `C:\Users\xie_f\projects\go\ai-creates-wealth` 当成本规划权威（Seq 行为分叉）。
