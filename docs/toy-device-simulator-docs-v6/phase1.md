# Phase 1 详细设计：单设备协议正确闭环（v6）

见 `architecture.md` 占用、先写不改、矩阵。

## 1. 目标

握手 → register ACK → report 回显（原子序号）→ pcm 上行 → 收 TTS。注入按矩阵。

## 2. 范围与非目标

**范围：** protocol、Reserved 状态机、pcm 分帧、夹具、`cmd/speak` `cmd/check` **`cmd/fixture`**、fault 矩阵、帧录制。

**非目标：** 批量、UI、Scenario、JSON ACK、非零 SleepMs、queue、线上 wav、CLI 查 Mongo。

## 3. 目录

```text
cmd/speak/
cmd/check/
cmd/fixture/          # alloc-fresh-id；Phase 1 必须交付
protocol/
core/
testdata/fixtures/fresh_ids.jsonl
testdata/audio/
```

## 4. protocol

100 字节头；golden 对照基线；禁止 mock.go。

## 5. 状态机

### 5.1 Connection

```text
Disconnected → Connecting → Connected
  → Registering → Registered      # /register/client code==0
  → Reporting → Ready             # /report/client 且 sequence_number 匹配
```

所有上行 report（首次、keepalive、若有手动）走 **同一原子递增器**。回显必须匹配该次发出的序号。

### 5.2 UplinkTurn

CAS Reserved → Speaking →（Stage=4：**先记 `vad`（若空）**）→ FinishingUpload → WaitingReply → Terminal。

取消表见架构 §4.5。Reserved 取消：`uplink_end_reason` **保持空**。

### 5.3 DownlinkPlayer

`NeedAck=1` → `'4'`，SleepMs=0（Phase 1 不测节流；0 的缓存语义见 Phase 2）。

## 6. Turn

Reserved 时分配 id/uuid。`uplink_end_reason` 先写不改。

## 7. 配置

与 v5 相同，`format: pcm`。`report_sequence_start` 为原子计数器初值（默认 1），之后所有 report +1。

## 8. CLI

```bash
go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
go run ./cmd/speak --config ... --inject skip_register --device-id <夹具id> --audio testdata/hello.wav --wait
go run ./cmd/check --config configs/example_device.yaml
```

speak 不查库。`bad_seq`：CAS Reserved 后发 Seq>=1，不断言「本地无 active」。

## 9. 验收

- [ ] register / 匹配序号的 report 回显 / pcm 上行 / TTS `tts_done`
- [ ] 首次 report 与 keepalive 序号递增且回显不串
- [ ] Reserved 时已有 turn_id
- [ ] Stage=4 后 `uplink_end_reason=vad`，补 Stage=2 后仍为 `vad`
- [ ] skip_register 用夹具 ID；bad_seq 本地已 Reserved 仍 drop
- [ ] `cmd/fixture` 可运行并写入 jsonl
- [ ] 线上 format=wav 拒绝
