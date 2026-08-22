# Phase 2 详细设计：多设备编排 + API + Scenario（审查后修订版）

> 已按两份 plan-review 关闭相关 P1 项。

## 1. 目标

在 Phase 1 基础上支持多设备批量操作、可编程 API、高层原语、Scenario 回归，以及连续模式基础能力。补齐 Phase 3 UI 所需的查询与配置保存接口。

## 2. 范围与非目标

**范围**

- Device Manager（生命周期、批量、模板、错峰、资源限制）
- REST + WebSocket 事件 API（含并发规则与 Turn 归属）
- Turn 完整结果返回
- `speak_and_wait` / `wait_for` 等高层原语
- Scenario YAML 执行器 + 简单报告
- 基础故障注入
- 流式发送器（语音段 + 精确 silence）
- playMode 热更新（report）
- ACK 完整支持（含 JSON 与 SleepMs 模拟）
- **Phase 3 所需读接口**：配置读写、Turn 历史、帧日志、音频下载

**非目标**

- Web UI 本身
- 完整 live mic
- 压测级上千连接优化
- server_observed_drop 探针（可选后置）

## 3. Device Manager 职责

- 创建 / 启动 / 停止 / 销毁设备实例
- 从模板批量生成 device_id
- 批量操作支持 stagger（错峰）
- 硬资源限制：max_connections、max_concurrent_speaking、per-device 缓冲上限
- 单设备异常 recover，不拖垮进程
- 事件总线聚合与过滤
- **单设备 Turn 并发控制**（见下节）

## 4. Turn 并发与归属规则（必须）

单设备同一时刻只允许一个 **active uplink turn**。

- 若已有 active turn，新的 `speak` / `speak_and_wait` 默认返回 **409 Conflict**
- 可配置策略：`reject`（默认）| `cancel_previous` | `queue`
- `interrupt` 只作用于当前 active turn
- `turn_id`（本地稳定 ID）与 `uplink_uuid`（协议 UUID）必须在 Turn 对象中显式保存，并在所有相关事件中带回
- 多个请求不得交叉发送帧或等待到错误的 Turn

## 5. 主要 API

**设备生命周期**

- `POST /devices` — 创建（支持批量/模板）
- `POST /devices/{id}/start`
- `POST /devices/{id}/stop`
- `DELETE /devices/{id}`
- `GET /devices` / `GET /devices/{id}`

**对话操作**

- `POST /devices/{id}/speak`
- `POST /devices/{id}/interrupt`
- `POST /devices/{id}/report`
- `POST /devices/{id}/speak_and_wait` — 返回完整 Turn 结果
- `POST /wait` — 通用 wait_for

**Turn 与查询（Phase 3 依赖，必须在本阶段交付）**

- `GET /devices/{id}/turns`
- `GET /devices/{id}/turns/{turn_id}`
- `GET /devices/{id}/turns/{turn_id}/frames`
- `GET /devices/{id}/turns/{turn_id}/audio/uplink`
- `GET /devices/{id}/turns/{turn_id}/audio/downlink`

**配置**

- `GET /devices/{id}/config`
- `PUT /devices/{id}/config` — 保存回存储/YAML

**Scenario**

- `POST /scenarios/run`
- `GET /scenarios/runs/{run_id}`

**故障注入**

- `POST /devices/{id}/faults`

**事件**

- WebSocket `/ws/events` — 订阅，支持过滤

## 6. Scenario YAML 示例（修订后）

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
      # asr_contains 仅当设备类型开启 StreamingAsrTextReply 时使用
      - type: asr_contains
        text: "你好"
        optional: true
```

主断言 = 匹配 UUID 的 TTS 完成 + turn_end_reason=idle。`asr_contains` 标为 optional。

## 7. 流式发送器与 silence 语义（写死）

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

**silence 精确定义：**

- `type: silence` 表示：按目标格式与 sample_rate，以与语音段相同的切片节拍，发送指定 duration 的**静音载荷**。
- PCM/WAV：全零采样。
- 已编码格式（mp3/amr 等）：必须能生成合法静音帧；若无法生成，该格式在配置阶段拒绝支持 silence。
- **不是**单纯 sleep 而不发帧。
- 收到 Stage=4 后：立即停止后续 stream 段，补发 Stage=2，结束本上行 Turn。

## 8. 故障注入清单（基础）

- 错误 Seq（非 0 起步）
- 跳过 register / report
- 错误 AudioHeader（长度/magic/字段）
- 超大 payload
- 重复 UUID
- 错误 Stage 序列

注入后应产生 `expected_server_drop`（inferred_no_reply）并带对应 `injected_fault`，而不是假的服务端内部原因事件。

## 9. 验收 Checklist

- [ ] 可同时启动可配置数量的设备并完成对话
- [ ] 批量操作支持错峰
- [ ] 同一设备并发 speak 时默认返回 409（或按配置策略正确处理）
- [ ] `speak_and_wait` 返回完整结构化 Turn，含 turn_id 与 uplink_uuid
- [ ] 事件可正确关联到 device_id + turn_id
- [ ] Phase 3 所需查询/配置/音频下载 API 全部可用
- [ ] 至少 2～3 个 Scenario 可自动跑通并产出 pass/fail 报告
- [ ] 故障注入能稳定触发 expected_server_drop + injected_fault
- [ ] silence 按真实节奏发送静音帧，Stage=4 后正确取消并补 Stage=2
- [ ] playingMode 可通过 report 热更新
- [ ] 单设备异常不影响其他设备
