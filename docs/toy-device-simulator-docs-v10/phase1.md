# Phase 1 详细设计：单设备协议正确闭环（v10）

完成矩阵、提前下行缓存、原子 report、register 超时、ACK 见 `architecture.md`。

## 1. 目标

握手 → register（超时或拒绝必收口）→ 初始 report 精确序号 → Ready → pcm 上行 → **WaitingReply 之后**按矩阵结束 Turn。  
binary ACK（音频+指令，SleepMs=0）。one-shot CLI。

## 2. 范围与非目标

**范围：** protocol；三套状态机；early_downlink_buf；`pending_reports` 锁内取号；`register_ack_timeout`；完成矩阵；fault；pcm；`cmd/speak|check|fixture`；binary ACK。

**非目标：** 批量 REST、UI、Scenario、JSON ACK、非零 SleepMs、queue、查 Mongo/DownlinkAck、改 device_id、静默探针。

## 3. 目录

`protocol/` `core/`（含 `pending_reports.go`、`early_downlink.go`、`event_log.go`）`cmd/speak|check|fixture/` `testdata/`

## 4. protocol

100 字节头；golden 对照基线；禁止 mock.go。解析 asr_result（含 IsFinal）、Code、chat_reply。编码 binary ACK。

## 5. 状态机

### 5.1 Connection

```text
Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready
```

- 进入 Registering（已发 register）启动 `register_ack_timeout_sec`（默认 5）。
- 无 ACK 到期 → `fail_connection(register_timeout)`，进程非零退出；`--wait` 不悬挂。
- `code != 0` → `ack_failure` 再 `fail_connection(register_nack)`。
- `skip_register`：不发 register，不启动该超时。
- 初始 report：锁内取号，首序号 = `report_sequence_start`。超时 → `fail_connection(report_timeout)`。
- 并发单测：两条 report 取号不得相同、不得覆盖 pending（可用测试桩并发调用发送路径）。

### 5.2 UplinkTurn

Ready 后 CAS（注入例外）。Stage=4 先 vad 再补 Stage=2。

**WaitingReply 前：** 终态下行入 `early_downlink_buf`，不 Terminal。失败 JSON 除外（停上行）。每帧 Stage=1 前检查 Terminal。

**进入 WaitingReply：** 回放缓冲再走矩阵。仅 **IsFinal=true** 可 silent；interim-only → timeout/drop。

### 5.3 ACK

NeedAck/need_ack ==1 → `'4'`。无真实指令时用 `testdata/inbound/` 验收编码。

## 6–7. Turn 与配置

```yaml
behavior:
  report_sequence_start: 1
  report_echo_timeout_sec: 5
  register_ack_timeout_sec: 5
  first_reply_timeout_sec: 20
  downlink_idle_timeout_sec: 20
  non_audio_followup_sec: 5
  post_final_asr_silence_sec: 5
  wait_timeout_slack_sec: 5
  downlink_ack: { mode: binary, sleep_ms: 0, code: 0 }
```

`format` 非 pcm、`mode: json`、`sleep_ms!=0` → 拒绝。`--wait` 用预算公式。

## 8. CLI

`cmd/fixture alloc-fresh-id`；`cmd/speak --wait`；skip_register 用夹具 ID 作**启动身份**。不查 Mongo。

## 9. 录制

`turn.json` 含 `reply_kind`、缓冲回放标记、IsFinal。

## 10. 验收

- [ ] 首个 report 序号 = start；并发取号无重复、无覆盖
- [ ] 无 register ACK：超时后退出，不卡死
- [ ] register code≠0：ack_failure 后退出
- [ ] 长音频期间注入 command：槽不提前释放；WaitingReply 后 followup；无双发 Stage=1
- [ ] 失败 JSON 在 Speaking：停上行并 Terminal error
- [ ] 仅 interim asr_result：timeout/drop，不是 silent idle
- [ ] IsFinal 且无终态下行：silent idle
- [ ] TTS / command / JSON / command+TTS 按矩阵
- [ ] drop 行 expected_server_drop；`--wait` 不悬挂
- [ ] binary ACK；Stage=4 vad 不被覆盖
- [ ] skip_register 读 jsonl
