# Phase 2 详细设计：多设备编排 + API + Scenario（v3）

## 1. 目标

在 Phase 1 基础上支持多设备批量、可编程 API、高层原语、Scenario，以及连续模式。补齐 Phase 3 所需查询与配置接口。注入路径规则与 Phase 1 相同。

## 2. 范围与非目标

**范围**

- Device Manager（生命周期、批量、模板、错峰、资源限制）
- REST + WebSocket 事件 API
- Turn 完整结果；`speak_and_wait` / `wait_for`
- Scenario YAML + 报告
- 基础故障注入（走注入路径）
- 流式发送器（语音段 + 精确 silence）
- playMode 热更新（report）
- ACK 完整支持（JSON 与 SleepMs 模拟）
- Phase 3 所需：配置读写、Turn 历史、帧日志、音频下载

**非目标**

- Web UI 本身、完整 live mic、上千连接压测
- `server_observed_drop` 探针（可选后置）

## 3. Device Manager

- 创建 / 启动 / 停止 / 销毁
- 模板批量生成 device_id；批量 stagger
- 硬限制：`max_connections`、`max_concurrent_speaking`、per-device 缓冲
- 单设备异常 recover
- 事件聚合过滤
- 单设备 Turn 并发控制（下节）

## 4. Turn 并发与归属

单设备同一时刻只允许一个 **active uplink turn**。

- 已有 active turn 时，新的 `speak` / `speak_and_wait` 默认 **409 Conflict**
- 策略：`reject`（默认）| `cancel_previous` | `queue`
- `cancel_previous`：**先发 Stage=3**，结束当前 Turn（`uplink_end_reason=interrupt`），**换新 UUID 且 Seq 从 0**，再开始新上行。禁止在同一 UUID 上交叉写帧。
- `interrupt` 只作用于当前 active turn（发 Stage=3，换 UUID）
- 事件必须带回 `turn_id` 与 `uplink_uuid`

## 5. 主要 API

**设备生命周期**

- `POST /devices`
- `POST /devices/{id}/start`
- `POST /devices/{id}/stop`
- `DELETE /devices/{id}`
- `GET /devices` / `GET /devices/{id}`

**对话**

- `POST /devices/{id}/speak`
- `POST /devices/{id}/interrupt`
- `POST /devices/{id}/report`
- `POST /devices/{id}/speak_and_wait`
- `POST /wait` — **必填 `device_id`**，可选 `turn_id`、`event_type`、`timeout`。禁止无设备作用域的全局等待。

**Turn 与查询（Phase 3 依赖，本阶段必须交付）**

- `GET /devices/{id}/turns`
- `GET /devices/{id}/turns/{turn_id}`
- `GET /devices/{id}/turns/{turn_id}/frames`
- `GET /devices/{id}/turns/{turn_id}/audio/uplink`
- `GET /devices/{id}/turns/{turn_id}/audio/downlink`

**配置**

- `GET /devices/{id}/config`
- `PUT /devices/{id}/config`

**Scenario / 注入 / 事件**

- `POST /scenarios/run`、`GET /scenarios/runs/{run_id}`
- `POST /devices/{id}/faults` — 启用注入路径（与 Phase 1 `--inject` 同一组 `injected_fault`）
- `GET /ws/events` — 订阅，过滤含 `device_id` / `turn_id`

## 6. Scenario 与 tts_done

```yaml
name: "basic_ptt_hello"
devices:
  - config: "configs/example_device.yaml"
steps:
  - action: start
  - action: speak
    audio: "testdata/hello.wav"
    wait: true
    timeout_sec: 30
  - assert:
      - type: event_received
        event: tts_done
      - type: turn_end_reason
        reason: idle
      - type: asr_contains
        text: "你好"
        optional: true
```

`tts_done` 定义（与 `architecture.md` §4.2 相同）：至少一帧匹配 UUID 的 `tts_chunk`，且 DownlinkPlayer 因 idle 回到 Idle。零下行超时不得判 `tts_done`。

主断言 = `tts_done` + `turn_end_reason=idle`。`asr_contains` 仅当 `StreamingAsrTextReply=true`。

## 7. 流式发送与 silence

```yaml
stream:
  - type: audio
    file: "speech_seg1.wav"
  - type: silence
    duration_ms: 1500
  - type: audio
    file: "speech_seg2.wav"
  - type: silence
    duration_ms: 3000
```

- `silence`：按目标格式与 sample_rate、与语音段相同切片节拍，发送指定 duration 的静音载荷。
- PCM/WAV：全零采样。已编码格式必须能生成合法静音帧，否则配置阶段拒绝。
- **不是**只 sleep 不发帧。
- 收到 Stage=4：停止后续 stream 段，补 Stage=2，结束本上行 Turn。

## 8. 故障注入

与 Phase 1 同一清单：`bad_seq`、`skip_register`、`skip_report`、`bad_header`、`oversize`、`dup_uuid`、错误 Stage 序列。

走注入路径：帧必须出站；结果为 `expected_server_drop` + `injected_fault`。  
本基线非 MH 的 `bad_seq` 期望为丢弃（见 `architecture.md` §10）。

## 9. 验收 Checklist

- [ ] 可同时启动可配置数量的设备并完成对话
- [ ] 批量错峰
- [ ] 同设备并发 speak 默认 409；`cancel_previous` 先 Stage=3 再换 UUID
- [ ] `speak_and_wait` 返回完整 Turn（含 `turn_id`、`uplink_uuid`）
- [ ] 事件关联 `device_id` + `turn_id`
- [ ] `POST /wait` 无 `device_id` 时拒绝
- [ ] Phase 3 所需查询/配置/音频下载 API 全部可用
- [ ] 2～3 个 Scenario 自动跑通并出 pass/fail 报告
- [ ] 注入触发 `expected_server_drop` + `injected_fault`，outbound 可在 frames 中查到
- [ ] silence 发静音帧；Stage=4 后取消并补 Stage=2
- [ ] playingMode 可通过 report 热更新
- [ ] 单设备异常不影响其他设备
