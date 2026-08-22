# Phase 1 详细设计：单设备协议正确闭环（v12）

本阶段边界不变：做完即交付 **握手 → register → report → pcm 上行 → 收回复 → CLI/落盘**。  
无 REST、无批量、无 JSON ACK。正确性条款在 §10.2。

## 1. 目标

one-shot CLI，playMode=1。keepalive 防 360s 踢线。每次 Turn 结束发 `turn_terminal`。

## 2. 范围与非目标

**范围：** protocol；三套状态机；writePump（有界队列）；读循环不阻塞；keepalive；pending_reports；register 先登记 timer 再发送且 **一次性消费**；early_downlink_buf；完成矩阵；`turn_terminal`；fault；pcm；完整 YAML；`cmd/speak|check|fixture`；binary ACK。

**非目标：** 批量 REST、UI、Scenario、JSON ACK、非零 SleepMs、queue、conn_permit、改 device_id。

## 3. 目录

`protocol/` `core/`（writePump、register_settle、turn_terminal）`cmd/speak|check|fixture/` `configs/example_device.yaml` `testdata/`

## 4–6. protocol 与状态机

100 字节头；golden 对照基线；禁止 mock.go。

```text
Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready
```

**register：** `conn_mu` 下 Registering + `register_attempt_id` + timer + settle=pending，**再** Enqueue。ACK 与 timeout callback 均持锁，校验 generation/attempt/`Connection==Registering`，`try_consume_register_settle` 赢家才能推进。`Timer.Stop` 不足以防旧 callback。

**report：** `report_mu` 取号登记后 **解锁**，再 Enqueue。首序号=start。

**Turn Terminal：** 设备锁内落库并 `turn_terminal`，再唤醒 `--wait`。

**writePump：** 深度 256；满则失败退出；关闭前 drain（2s）。

**读循环 / keepalive：** 同架构 §4.12–4.13。

## 7. 配置

```yaml
device:
  enterprise: "demo"
  device_type: "A3"              # 禁止 MH 前缀
  device_id: "sim_001"
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1                # ∈ {1,2,3}

  audio:
    format: "pcm"
    sample_rate: 16000
    channels: 1
    sample_format: "s16le"
    slice_ms: 100
    max_payload_size: 51200

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report"
    report_sequence_start: 1
    report_echo_timeout_sec: 5
    register_ack_timeout_sec: 5
    first_reply_timeout_sec: 20
    downlink_idle_timeout_sec: 20
    non_audio_followup_sec: 5
    post_final_asr_silence_sec: 5
    wait_timeout_slack_sec: 5
    write_queue_depth: 256
    write_drain_timeout_sec: 2
    expect_downlink_need_ack: false
    downlink_ack: { mode: binary, sleep_ms: 0, code: 0 }

  uuid: { min: 1, max: 2147483647 }
  server: { url: "ws://127.0.0.1:8089/" }
  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings"
```

## 8–9. CLI 与录制

```bash
go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait
```

`--wait` 等 `turn_terminal`。落盘不阻塞读循环。

## 10. 验收

### 10.1 交付

- [ ] 握手→register code=0→report Ready→pcm 上行→合法回复
- [ ] CLI 与落盘；keepalive 360s 不掉线；读循环不堵
- [ ] skip_register jsonl+drop；skip_report 不要求 drop

### 10.2 正确性附录

- [ ] 立即 ACK 不误杀；无 ACK/nack 退出
- [ ] ACK 与 timeout **同时**触发：只一次状态迁移；不会 Registered 后再 fail
- [ ] 所有终态（TTS/command/JSON/silent/timeout/interrupt）都有 `turn_terminal`
- [ ] writePump 保序；report_mu 不跨 Enqueue
- [ ] 提前下行回放无二次 ACK；仅 IsFinal 可 silent
