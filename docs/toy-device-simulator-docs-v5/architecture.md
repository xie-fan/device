# 玩具设备模拟器 — 整体架构设计文档（v5）

> 吸收 v4 审查。Turn 占用必须在受理时原子占用；skip_register 不靠 CLI 查 Mongo。

## 1. 背景与目标

模拟玩具 WebSocket `Action=chatbot` 全路径：单设备闭环、批量、Agent API、调试 UI。

协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。

## 2. 设计原则

1. 协议忠实；mock.go 仅缺陷清单。
2. 失败按 fault 矩阵推断，禁止凡注入必 drop。
3. Turn 一等公民：`uplink_end_reason` / `turn_end_reason` 分离；已发生的上行收口不改写。
4. **Turn 占用原子**：请求受理即 Reserved，不是第一帧发出才占用。
5. API 先于 UI。
6. 内部 PCM 唯一时间轴；Phase 1/2 线上格式强制 **pcm**。
7. 状态机正交。

## 3. 总体架构

```text
Control Plane: UI | REST+WS | CLI / Scenario / 夹具
Device Manager: 生命周期 / 每设备 Turn 槽（CAS Reserved）/ 注入
DeviceInstance: Connection + UplinkTurn + DownlinkPlayer
protocol + 内部 PCM 分帧
recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid      # Reserved 时分配，发帧前已存在
├── state                      # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── injected_fault
├── uplink_end_reason          # 一旦因 Stage=2/3/5/vad 赋值，禁止改写
├── turn_end_reason            # 赋值即 Terminal，释放槽
└── chunks / tts / events ...
```

**占用规则（写死）**

- 每设备一个槽：`idle` 或指向唯一非 Terminal Turn。
- `speak` / `speak_and_wait` / 注入发音频：**受理时 CAS** 创建 `Reserved`（分配 `turn_id`、`uplink_uuid`）。成功才返回 2xx/开始发帧。
- CAS 失败：默认策略 **reject → 409**。槽含 Reserved/Speaking/FinishingUpload/WaitingReply（PlayingTTS 期间 UplinkTurn 为 WaitingReply）。
- **禁止**以「第一帧是否已发出」判断占用。两个并发 speak 必须一个 Reserved、一个 409。

**active** = 槽非空（Reserved 起到 Terminal 之前）。

### 4.2 Event

事件带 `device_id` + `turn_id`（无 Turn 时 `correlation_id`）。

| 类型 | 含义 |
|------|------|
| `local_validation_error` | 正常路径非法，帧未发 |
| `expected_server_drop` | 已发帧，timeout 无匹配 ASR/TTS；仅矩阵该行写 drop 时作通过条件 |
| `protocol_error` | 无前缀 JSON `Code=1`/`14007` |
| `ack_failure` | **仅** `/register/client` 且 `data.code != 0` |
| `report_echo` | `/report/client` + `ReportData` 回显，且 **`data.sequence_number` 等于本次上行 report** |

`tts_chunk` / `tts_done` 定义同 v4：至少一帧匹配 UUID 的 TTS 且下行 idle 才是 `tts_done`。

### 4.3 Scenario

主断言 `tts_done` + `turn_end_reason=idle`。注入 Scenario 按矩阵。

### 4.4 注入 fault 矩阵

基线：`LookupCachedDevicePlayMode` 先内存再 Mongo `status=1`，与本连接是否 register 无关。`ParseAudioHeader` 只拒 `len<100`。

**全新 ID 的证明（skip_register 唯一前置）**

模拟器 **没有** Mongo/core 查询通道，**禁止** CLI/API 自行判断 `status=1`。

由 **测试夹具** 负责：

1. 使用隔离 core（空 device 表，或约定前缀仅本套件写入）。
2. 生成 `device_id = sim_sr_{run_uuid}_{n}`（run_uuid 为本趟 UUIDv4）。
3. 套件 setup **不得**对该 ID 执行 register；并在 `testdata/fixtures/fresh_ids.jsonl` 记下 `{id, run_uuid, isolation}`。
4. **证明**：隔离环境 + 生成规则；不是 speak 进程查库。

误用已存在 ID 做 skip_register：测试无效。夹具/Scenario 的 setup 负责，speak **不**做「已存在则拒绝」。

| fault | 前置 | 出站 | 期望 |
|-------|------|------|------|
| `skip_register` | 夹具新鲜 ID（上） | 握手后不 register；合法音频+Stage=2 | `expected_server_drop` |
| `skip_report` | 任意；可先 register | register 成功；不 report；合法音频 | **不默认 drop**；允许 TTS；停在 Registered |
| `bad_seq` | 非 MH；无 active（含 Reserved） | Stage=1 Seq>=1，头 100B | `expected_server_drop` |
| `oversize` | 注入 | 头合法 + payload>51200 | `expected_server_drop` |
| `bad_header` | 注入 | `'0'`+其后 **<100** 字节 | `expected_server_drop` |
| `dup_uuid` / `bad_stage` | — | 可录帧 | **不作 drop 验收** |

### 4.5 取消转移表（interrupt / cancel_previous）

新 speak 在 CAS 前若策略为 `cancel_previous`：对**当前槽内 Turn**按行执行，待其 Terminal 并释放槽后，再 CAS 新 Reserved。

| 当前状态 | 出站 | uplink_end_reason | turn_end_reason |
|----------|------|-------------------|-----------------|
| Reserved（尚未发任何 `'0'`） | 不发 Stage=3 | 不赋值或 `interrupt` | `interrupt`（立即 Terminal） |
| Speaking | 发 Stage=3；停后续 Stage=1 | `interrupt` | `interrupt` |
| FinishingUpload，Stage=2 **未**发出 | 不发 Stage=2；发 Stage=3 | `interrupt` | `interrupt` |
| FinishingUpload，Stage=2 **已**发出 | 发 Stage=3 停 TTS | **保持 `stage2`** | `interrupt` |
| WaitingReply / 下行 PlayingTTS | 发 Stage=3 | **保持 `stage2`** | `interrupt` |
| Terminal | 无操作 | 不变 | 不变 |

然后：新 UUID、Seq=0、新 Reserved。禁止同一 UUID 交叉写帧。

## 5. 协议硬约束

| 约束 | 要求 |
|------|------|
| 收包 | 按字节收 Text |
| 头 | 100 字节；golden 对照基线 |
| 占用 | 受理 CAS Reserved |
| Report | `/report/client` 且 `sequence_number` 匹配才 Ready |
| 线上格式 Phase 1/2 | **pcm**（raw s16le mono）；WAV 仅源文件 |
| 编码顺序 | 内部 PCM 时间轴 → **至多一个编码器输出一整段字节流** → 再按 ≤50KiB 切 payload。禁止对每一片 PCM 单独封装 WAV |
| ACK Phase 1 | `NeedAck=1` → `'4'`，SleepMs=0 |
| ACK Phase 2 | 见 `phase2.md` 契约（binary/json、SleepMs、节流验收） |

## 6. 音频管线（写死）

```text
源文件（wav 则解 RIFF）→ 内部 PCM（mono s16le, sample_rate）
  → 拼接/插入 PCM silence
  → Phase 1/2：不再编码容器，按 slice_ms 把 PCM 字节切成帧 payload（format=pcm）
  → Phase 4 若允许 mp3/wav 上线：对整段 PCM 只跑一次编码器，再切编码流
```

默认与示例：`audio.format: pcm`。配置写成 `wav` 作为**线上**格式时，Phase 1/2 **启动拒绝**（`local_validation_error`），避免逐片 RIFF。

## 7. 技术选型

推荐 Go；真实 pcm/TTS 收包冒烟后锁定。

## 8. 阶段

| 阶段 | 交付 |
|------|------|
| Phase 1 | protocol、Reserved 状态机、夹具新鲜 ID、pcm 分帧、one-shot |
| Phase 2 | Manager、REST；**默认 reject，可选 cancel_previous，不交付 queue**；ACK 契约；查询 API |
| Phase 3 | UI |
| Phase 4 | queue；线上非 pcm；可选 Mongo 预检 |

## 9. 目录

`protocol/` `core/` `cmd/speak` `testdata/fixtures/fresh_ids.jsonl` `testdata/audio/` `recordings/{device_id}/{turn_id}/`

## 10. 对齐基线

| 项 | 值 |
|----|----|
| 仓库 | `C:\Users\xie_f\projects\other\ai-creates-wealth` |
| commit | `5a02d70cdf964bdafea7be92495ad1d0a63499c5` |
| MH Seq 例外 | 是 |
| 非 MH 无活跃 turn Seq>0 | 丢弃 |
| 音频准入 | 缓存或 DB `status=1` |
| 头解析 | `len<100` 失败 |
| report | `ReportData` 回显 |
| ACK 节流 | `SleepMs>0` 时 `ThrottleBeforeDownlink` 在后续下发前 sleep（`speedCtrl` 默认开，上限默认 6000ms） |
| core | `ws://127.0.0.1:8089/` |
