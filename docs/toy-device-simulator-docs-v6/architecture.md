# 玩具设备模拟器 — 整体架构设计文档（v6）

> 吸收 v5 审查。`uplink_end_reason` 先写不改；ACK SleepMs=0 不能当清缓存。

## 1. 背景与目标

模拟 WebSocket `Action=chatbot`：单设备闭环、批量、Agent API、调试 UI。  
协议：`docs/toy-device-websocket-protocol.md` + §10 对齐基线。

## 2. 设计原则

1. 协议忠实；mock.go 仅缺陷清单。
2. 失败按 fault 矩阵推断。
3. Turn 一等公民；**`uplink_end_reason` 先写不改**（含 `vad`）。
4. Turn 占用在受理时 CAS Reserved。
5. API 先于 UI。
6. Phase 1/2 线上 **pcm**。
7. 状态机正交。

## 3. 总体架构

```text
Control Plane: UI | REST+WS | CLI / Scenario / cmd/fixture
Device Manager: 生命周期 / 每设备 Turn 槽 CAS / 注入
DeviceInstance: Connection + UplinkTurn + DownlinkPlayer
protocol + 内部 PCM 分帧
recordings/{device_id}/{turn_id}/
```

## 4. 核心抽象

### 4.1 Turn 与占用

```text
Turn
├── turn_id / uplink_uuid      # Reserved 时分配
├── state                      # Reserved | Speaking | FinishingUpload | WaitingReply | Terminal
├── injected_fault
├── uplink_end_reason          # 空 | stage2 | interrupt | vad | error | timeout
│                              # 非空后禁止改写（含随后的 Stage=2 / 取消）
├── turn_end_reason            # 赋值即 Terminal，释放槽
└── chunks / tts / events ...
```

**占用：** 每设备一槽。`speak` 受理时 CAS 创建 Reserved。失败默认 409。禁止用「第一帧是否已发」判断占用。  
**active（本地槽）** = 非 Terminal。与「服务端是否有 ASR session」不是同一回事。

### 4.2 `uplink_end_reason` 写入规则（先写不改）

| 触发 | 若当前为空 | 若已非空 |
|------|------------|----------|
| 收到 Stage=4 | **立即**记 `vad`，再停发并补 Stage=2 | 保持原值 |
| 发出 Stage=2 | 记 `stage2` | **保持**（VAD 后补 Stage=2 仍是 `vad`） |
| 本地发出 Stage=3/5 | 记 `interrupt` | 保持 |
| 取消表 | 仅 Reserved：保持空；其余按表，但非空则仍保持 | 保持 |

`turn_end_reason` 可在取消时设为 `interrupt`，与上行收口独立。

### 4.3 Event

带 `device_id` + `turn_id`。`ack_failure` 仅 register ACK。  
`report_echo`：`/report/client` + `ReportData` + **`sequence_number` 等于该连接原子计数器刚发出的那一次**。

`tts_done`：至少一帧匹配 UUID 的 TTS 且下行 idle。

### 4.4 注入矩阵

skip_register 仍由夹具证明全新 ID，speak 不查库。

| fault | 前置 | 出站 | 期望 |
|-------|------|------|------|
| `skip_register` | 夹具新鲜 ID | 不 register；合法音频+Stage=2 | drop |
| `skip_report` | 可先 register | 不 report；合法音频 | **不默认 drop** |
| `bad_seq` | 非 MH；**请求前本地槽空，CAS 成 Reserved 后再发**；**发帧时服务端无 active ASR session**（本轮未发 Seq=0）。本地发帧时槽已非空，这是预期 | Stage=1 Seq>=1，头 100B | drop |
| `oversize` | 注入 | payload>51200 | drop |
| `bad_header` | 注入 | `'0'`+<100B | drop |
| `dup_uuid` / `bad_stage` | — | 可录帧 | 不作 drop 验收 |

### 4.5 取消转移表

执行前：若 `uplink_end_reason` 已非空，**本表不得改写它**（只可能改 `turn_end_reason` 与出站）。

| 当前状态 | 出站 | 空的 uplink_end_reason 变为 | turn_end_reason |
|----------|------|------------------------------|-----------------|
| Reserved（未发 `'0'`） | 不发 Stage=3 | **保持空（选定：不赋值）** | `interrupt`，立即 Terminal |
| Speaking | Stage=3，停 Stage=1 | `interrupt` | `interrupt` |
| FinishingUpload，Stage=2 未发 | 不发 Stage=2；发 Stage=3 | `interrupt`（若已因 VAD 为 `vad` 则保持 `vad`） | `interrupt` |
| FinishingUpload，Stage=2 已发 | Stage=3 停 TTS | 保持已有（`vad` 或 `stage2`） | `interrupt` |
| WaitingReply / PlayingTTS | Stage=3 | 保持已有 | `interrupt` |
| Terminal | 无 | 不变 | 不变 |

然后新 UUID、Seq=0、新 Reserved。

## 5. 硬约束

占用 CAS Reserved；report 匹配序号；线上 pcm；Phase 1 ACK binary SleepMs=0（见下：0 不清缓存）；Phase 2 ACK 契约见 `phase2.md`。

**report 序号：** 每个 DeviceInstance **一个原子递增器**。初始 report、周期 keepalive report、`POST /report` **全部**从该计数器取值，禁止各写各的导致回显串台。

## 6. 音频管线

源 wav 解封装 → 内部 PCM（mono s16le）→ silence 为零采样 → 按 slice_ms 切 pcm 字节上线。Phase 1/2 拒绝线上 `format=wav`。

## 7. 技术选型

推荐 Go；真实 pcm/TTS 冒烟后锁定。

## 8. 阶段

| 阶段 | 交付 |
|------|------|
| Phase 1 | protocol、Reserved、`cmd/speak` `cmd/check` **`cmd/fixture`**、pcm、矩阵 |
| Phase 2 | Manager、REST；默认 reject，可选 cancel_previous，不交付 queue；音频+指令 ACK；查询 API |
| Phase 3 | UI |
| Phase 4 | queue；非 pcm；可选清 Redis 节流缓存 |

## 9. 目录

`cmd/speak` `cmd/check` `cmd/fixture` `testdata/fixtures/fresh_ids.jsonl` `protocol/` `core/` `recordings/{device_id}/{turn_id}/`

## 10. 对齐基线

| 项 | 值 |
|----|----|
| 仓库 | `C:\Users\xie_f\projects\other\ai-creates-wealth` |
| commit | `5a02d70cdf964bdafea7be92495ad1d0a63499c5` |
| MH Seq 例外 | 是 |
| 非 MH 无 **服务端** ASR session 且 Seq>0 | 丢弃 |
| ACK 状态 | `UpdateMemoryState`：SleepMs 与 MemoryPercent 皆 ≤0 **跳过写入**，旧正值保留至 TTL（默认 60s） |
| 节流 | `SleepMs>0` 时后续下发前 sleep，封顶默认 6000ms |
| core | `ws://127.0.0.1:8089/` |
