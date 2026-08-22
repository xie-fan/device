# Phase 2 详细设计：多设备编排 + API + Scenario

## 1. 目标

在 Phase 1 基础上支持多设备批量操作、可编程 API、高层原语、Scenario 回归，以及连续模式基础能力。

## 2. 范围与非目标

**范围**

- Device Manager（生命周期、批量、模板、错峰、资源限制）
- REST + WebSocket 事件 API
- Turn 完整结果返回
- `speak_and_wait` / `wait_for` 等高层原语
- Scenario YAML 执行器 + 简单报告
- 基础故障注入
- 流式发送器（语音段 + 静音段时间轴）
- playMode 热更新（report）
- ACK 支持

**非目标**

- Web UI
- 完整 live mic
- 压测级上千连接优化
- SQLite 历史查询

## 3. Device Manager 职责

- 创建 / 启动 / 停止 / 销毁设备实例
- 从模板批量生成 device_id
- 批量操作支持 stagger（错峰）
- 硬资源限制：`max_connections`、`max_concurrent_speaking`、per-device 缓冲上限
- 单设备异常 recover，不拖垮进程
- 事件总线聚合与过滤（按 device / tag / enterprise）

## 4. 主要 API（草案）

**设备生命周期**

- `POST /devices` — 创建（支持批量/模板）
- `POST /devices/{id}/start`
- `POST /devices/{id}/stop`
- `DELETE /devices/{id}`
- `GET /devices` / `GET /devices/{id}`

**对话操作**

- `POST /devices/{id}/speak` — 上传文件或指定 stream_spec
- `POST /devices/{id}/interrupt`
- `POST /devices/{id}/report` — 热更新 playingMode 等
- `GET /devices/{id}/turns/latest`
- `GET /devices/{id}/turns/{turn_id}`

**高层**

- `POST /devices/{id}/speak_and_wait` — 返回完整 Turn 结果
- `POST /wait` — 通用 wait_for（event_type + filters + timeout）

**Scenario**

- `POST /scenarios/run` — 执行单个或批量 Scenario
- `GET /scenarios/runs/{run_id}`

**故障注入**

- `POST /devices/{id}/faults` — 注入坏头、错误 Seq、跳过 register、超大 payload 等

**事件**

- WebSocket `/ws/events` — 订阅，支持过滤

## 5. Scenario YAML 示例

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
      - type: asr_contains
        text: "你好"
      - type: event_received
        event: tts_done
      - type: turn_finish_reason
        reason: idle
```

常见断言类型：

- `asr_contains` / `asr_equals`
- `event_received`（tts_done / vad / command / silent_drop / ...）
- `error_code`
- `timeout`
- `turn_finish_reason`
- `response_time_lt`

## 6. 流式发送器（连续模式基础）

支持描述时间轴：

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

用于主动触发或不触发服务端 VAD，验证 Stage=4 → 补 Stage=2 路径。

## 7. 故障注入清单（基础）

- 错误 Seq（非 0 起步）
- 跳过 register / report
- 错误 AudioHeader（长度/magic/字段）
- 超大 payload
- 重复 UUID
- 错误 Stage 序列
- 模拟 status≠1（通过配置或注入）

## 8. 验收 Checklist

- [ ] 可同时启动可配置数量的设备并完成对话
- [ ] 批量操作支持错峰，不会瞬间打满
- [ ] `speak_and_wait` 返回完整结构化 Turn
- [ ] 事件可正确关联到 device_id + turn_uuid
- [ ] 至少 2～3 个 Scenario 可自动跑通并产出 pass/fail 报告
- [ ] 故障注入能稳定触发并被观测到
- [ ] 流式时间轴 + Stage=4 响应路径可复现
- [ ] playingMode 可通过 report 热更新
- [ ] 单设备异常不影响其他设备
