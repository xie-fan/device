# Phase 2 详细设计：多设备编排 + API + Scenario（v4）

注入期望仍以 `architecture.md` §4.4 矩阵为准，禁止「凡注入必 drop」。

## 1. 目标

批量设备、Control API、Scenario、连续模式时间轴。补齐 Phase 3 查询接口。

## 2. 范围与非目标

**范围：** Manager、REST+WS、`speak_and_wait`、`wait_for`（必填 `device_id`）、Scenario、faults API（矩阵内 fault）、内部 PCM 时间轴 + silence、report 热更新 playingMode、JSON ACK 与 SleepMs 模拟、Turn/帧/音频查询、并发策略 **reject** 与修正后的 **cancel_previous**。

**非目标：** Web UI、live mic、上千连接、`server_observed_drop`、**queue**（缺最大长度/取消/超时，推迟 Phase 4）、`dup_uuid`/`bad_stage` 的 drop 验收。

## 3. Device Manager

创建/启停/销毁、模板展开、错峰、`max_connections` / `max_concurrent_speaking`、单设备 recover、事件过滤、Turn 并发（下节）。

## 4. Turn 并发与归属

单设备同一时刻一个 **active turn**（定义见架构 §4.1：直到 `turn_end_reason` 赋值，含 WaitingReply / PlayingTTS）。

- 已有 active turn：新 `speak` / `speak_and_wait` 默认 **409**（策略 `reject`）
- 本阶段交付：`reject`（默认）与 `cancel_previous`
- **不交付 `queue`**

**`cancel_previous`**

1. 向当前 active turn 发 Stage=3（停 TTS / 停未完成上行）。
2. 原因字段：
   - 若尚未发完上行（仍在 Speaking）：`uplink_end_reason=interrupt`，`turn_end_reason=interrupt`
   - 若已发 Stage=2（WaitingReply 或 PlayingTTS）：**保持 `uplink_end_reason=stage2`**，只设 `turn_end_reason=interrupt`
3. 换 **新 UUID**，Seq 从 0，再开始新上行。禁止同一 UUID 交叉写帧。

`interrupt` API 同样遵守第 2 条，不篡改已完成的 `stage2` 上行事实。

## 5. 主要 API

生命周期：`POST/GET/DELETE /devices`，`/start` `/stop`。

对话：`/speak` `/interrupt` `/report` `/speak_and_wait`；`POST /wait` 必填 `device_id`，可选 `turn_id`。

查询：turns / frames / audio uplink&downlink；`GET/PUT /devices/{id}/config`。

Scenario：`POST /scenarios/run`，`GET /scenarios/runs/{run_id}`。

**`POST /devices/{id}/faults`**

- 必须在对应生命周期步骤 **之前** 配置，或先 stop 再 start（断线重建）。
- 设备已走过 register 再设 `skip_register`：返回 409/400，不得静默忽略。
- 已 Ready 再设 `skip_report`：无意义，拒绝。
- fault 取值与期望见架构矩阵；不要在 handler 里写死「必定 drop」。

事件：`GET /ws/events`，过滤 `device_id` / `turn_id`。

## 6. Scenario 与 tts_done

与 v3 相同：主断言 `tts_done` + `turn_end_reason=idle`；`asr_contains` optional。  
`tts_done`：至少一帧匹配 UUID 的 TTS 且下行 idle。零下行不是 `tts_done`。

注入类 Scenario 必须按矩阵写 assert，例如 `skip_report` 不得 assert `expected_server_drop`。

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

**唯一实现：**

1. 每段 `audio` 文件若为 WAV：去掉 RIFF/fmt/data 容器，得到 PCM。
2. 转到内部 PCM：**mono、s16le、`sample_rate`**（与 Phase 1 配置相同）。多声道则 downmix 或拒绝。
3. `silence`：在内部 PCM 上生成 `duration_ms` 的全零采样，**不是** sleep，也不是拼接另一个 WAV 文件。
4. 整条时间轴在内部 PCM 上拼接后再按 `slice_ms` 切片、编码为线上 `format` 发出。
5. 线上已编码格式若无法生成合法静音帧：配置阶段拒绝 silence。
6. Stage=4：停止后续段，补 Stage=2。

禁止：把 `speech_seg1.wav` 与 `speech_seg2.wav` 的文件字节直接相连。

## 8. 故障注入

仅矩阵内 fault。Phase 2 批量时 `skip_register` 仍要求每个目标 ID 确认不在库。  
`dup_uuid` / `bad_stage`：可注入并录帧，**不进入 pass/fail 的 drop 断言**。

## 9. 验收 Checklist

- [ ] 多设备对话；批量错峰
- [ ] 同设备第二次 speak 在 active（含 WaitingReply）时默认 409
- [ ] `cancel_previous`：已 Stage=2 时 `uplink_end_reason` 仍为 `stage2`，`turn_end_reason=interrupt`，新 UUID
- [ ] 不提供可用的 `queue`（或未实现并在文档/能力列表中标明）
- [ ] `speak_and_wait` 含 `turn_id` / `uplink_uuid`
- [ ] `POST /wait` 无 `device_id` 拒绝
- [ ] faults 在错误时机返回错误
- [ ] 查询/配置/音频 API 齐套
- [ ] Scenario：正常路径 `tts_done`+idle；注入路径按矩阵
- [ ] silence：内部 PCM 全零，frames 中无重复 RIFF 头
- [ ] Stage=4 取消时间轴并补 Stage=2
- [ ] report 热更新 playingMode 后行为变化可测
- [ ] 单设备异常不拖垮进程
