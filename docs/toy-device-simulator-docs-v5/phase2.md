# Phase 2 详细设计：多设备编排 + API + Scenario（v5）

占用 CAS、取消表、pcm 管线见 `architecture.md`。  
并发策略写死：**默认 reject，可选 cancel_previous，不交付 queue**。

## 1. 目标

批量、Control API、Scenario、连续 PCM 时间轴、ACK/SleepMs 契约、Phase 3 查询接口。

## 2. 范围与非目标

**范围：** Manager、REST+WS、`speak_and_wait`、`wait_for`（必填 `device_id`）、Scenario、faults（矩阵+夹具 ID）、PCM 时间轴、report 热更新、**ACK 契约（本节）**、查询 API、reject + cancel_previous。

**非目标：** UI、live mic、queue、线上非 pcm、Mongo 预检、`dup_uuid` drop 验收。

## 3. Device Manager

启停、模板、错峰、资源上限、每设备 **Turn 槽 CAS**（架构 §4.1）。

## 4. 并发

- 默认 `reject`：槽非空 → 409，不发帧。
- 可选 `cancel_previous`：按架构 §4.5 全表转移（含 Reserved、FinishingUpload），释放槽后再 CAS 新 Reserved。
- 不交付 `queue`。

`interrupt` 遵守同一张取消表。

验收必须覆盖：两个并发 HTTP speak 只有一个 Reserved；FinishingUpload 取消时 Stage=2 已发/未发两行。

## 5. API

生命周期、对话、turns/frames/audio、config：同 v4。`POST /wait` 必填 `device_id`。

**`POST /devices/{id}/faults`**

- 须在对应步骤前或 stop/start 重建。
- `skip_register` 的 ID 必须来自夹具新鲜 ID 字段（请求体带 `device_id` 覆盖或先 PUT config）；**服务端模拟器仍不查 Mongo**。
- 已 register 再设 skip_register → 400/409。

## 6. Scenario

`tts_done` + idle。skip_report 不得 assert drop。  
skip_register 步骤前必须有 `action: alloc_fresh_id`（夹具）。

## 7. 流式发送与 silence

源 wav 解封装 → 内部 PCM → 插入全零 PCM silence → **线上 pcm：按 slice_ms 切 PCM 字节成帧**。  
无「逐片 WAV 编码」。上行 `frames.jsonl` 音频 payload 不得出现第二个 RIFF。

Stage=4：停后续 PCM，补 Stage=2。

## 8. ACK 契约（本阶段必须交付）

仅当设备类型 `DownlinkAck=true` 且下行 `NeedAck=1`（音频）或指令 `need_ack=1`。否则不发 ACK。

### 8.1 配置

```yaml
behavior:
  downlink_ack:
    mode: binary          # binary | json
    sleep_ms: 0           # 写入 ACK 的 SleepMs；0 表示不节流
    code: 0
```

- `mode=binary`：发 `'4'` + 28 字节小端。
- `mode=json`：发 `'1'` + topic `{ent}/{type}/{id}/downlink-ack/server`。
- Phase 1 等价于 `binary` + `sleep_ms: 0`。本阶段允许改 `sleep_ms` 与 `mode`。

### 8.2 字段映射

对**刚收到的那一帧**下行音频：

| binary 字段 | 取值 |
|-------------|------|
| Ack | 该帧 `SequenceNumber` |
| DownlinkType | TTS=1；提示音=2；其它按协议 §10.1 |
| Code | 配置 `code`（默认 0） |
| SleepMs | 配置 `sleep_ms` |
| UUID | 不在 28 字节结构内；JSON 模式才带 `uuid` |

JSON：

| 字段 | 取值 |
|------|------|
| ack | 下行 Seq |
| downlink_type | `tts` / `hint_audio` / `command` / `unknown` … |
| uuid | 下行头 UUID |
| sequence_number | 下行 Seq |
| sleep_ms | 配置值 |
| code | 配置值 |

`NeedAck=1` 必须 ACK；与 `expect_downlink_need_ack` 无关。

### 8.3 节流验收

基线：`SpeedCtrlEnabled` 默认 true；`ThrottleBeforeDownlink` 在**后续下发前**按最近 ACK 的 SleepMs 等待，封顶默认 6000ms。MemoryPercent=0 时走 SleepMs 分支。

验收（设备类型须开 DownlinkAck）：

1. `sleep_ms: 0`：记录 TTS 分片到达间隔 T0（服务端另有约 300ms 片间隔）。
2. 同音频再跑 `sleep_ms: 500`（binary 或 json 各至少一次）：从**第二片**起到达间隔应相对 T0 **增大约 500ms**（允许抖动，但须显著大于 T0 且不超过 500ms+封顶+片间隔）。
3. `frames.jsonl` 有对应 `'4'` 或 `downlink-ack` 出站，SleepMs 字段为 500。

## 9. 故障注入

同架构矩阵。批量 skip_register：每个 ID 都来自夹具，不查库。

## 10. 验收 Checklist

- [ ] 多设备；错峰
- [ ] 并发 speak：CAS 一个 Reserved、一个 409（reject）
- [ ] cancel_previous 覆盖 Reserved / Speaking / FinishingUpload（Stage=2 已发与未发）/ WaitingReply
- [ ] 已 Stage=2：`uplink_end_reason=stage2`，`turn_end_reason=interrupt`
- [ ] 无 queue API 或明确 501
- [ ] speak_and_wait 含 turn_id（受理时即有）
- [ ] wait 无 device_id 拒绝
- [ ] faults 错误时机 4xx
- [ ] 查询 API 齐套
- [ ] Scenario 按矩阵
- [ ] 时间轴 pcm 分帧，无重复 RIFF
- [ ] ACK binary/json + SleepMs 节流验收通过
- [ ] report 热更新 playingMode
- [ ] 单设备异常不拖垮进程
