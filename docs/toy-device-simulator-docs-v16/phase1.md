# Phase 1 详细设计：单设备协议正确闭环（v16）

阶段边界不变。做完即交付握手→register→report→pcm 上行→回复→CLI/落盘。无 REST、无 speak backlog。

本阶段不增加产品能力。`finalize_started` 后只有进程收口能 Terminal，writePump **不**回锁看 Turn。同目录 `architecture.md` 为完整契约。

## 1–2. 目标与范围

one-shot CLI，playMode=1。keepalive。`turn_terminal`。退出 wait `finalize_done`。

**范围：** protocol；状态机；outbound buffer + BeginClose(token)；读循环不堵；report 解锁再 enqueue；register 一次性消费；完成矩阵；fault；YAML；`cmd/speak|check|fixture`；binary ACK。

**非目标：** REST/UI/Scenario/JSON ACK/非零 SleepMs/speak backlog/conn_permit/改 device_id。

## 3–5. 目录与状态机

`protocol/` `core/` `cmd/speak|check|fixture/` `configs/example_device.yaml` `testdata/`

```text
Disconnected → Connecting → Connected → Registering → Registered → Reporting → Ready
收口：Disconnecting → BeginClose(token) → closer drain → Disconnected
```

锁：`device_mu` → `conn_mu` → `report_mu`。泵不回锁。先读本地 WAV 再 CAS。`--wait` 预算含 `post_final_asr_silence`。

## 6. 矩阵（本阶段正文）

关联/计时/终止/取消/fault 与架构 §4.4–4.7 相同：TTS 看 UUID；仅 IsFinal 可 silent；Reserved 不发 Stage=3；skip_register drop；skip_report 不默认 drop；bad_seq/oversize/bad_header drop。夹具 `sim_sr_{run_uuid}_{n}`。

## 7. 配置

```yaml
device:
  enterprise: "demo"
  device_type: "A3"
  device_id: "sim_001"
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"
  playing_mode: 1
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

## 8–10. CLI、录制、验收

`go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait`  
落盘 `recordings/{device_id}/{turn_id}/`（Phase 1 单进程无 instance 目录亦可；Phase 2 才强制 `/{instance_id}/`）。

交付：主路径、落盘、keepalive、skip_register jsonl。正确性：ACK∥timeout；token Stage 3 不回锁；解码失败不占槽；全部 `turn_terminal`。
