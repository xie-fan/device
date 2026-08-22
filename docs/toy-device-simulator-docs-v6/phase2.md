# Phase 2 详细设计：多设备编排 + API + Scenario（v6）

占用、取消表、先写不改见 `architecture.md`。  
并发：**默认 reject，可选 cancel_previous，不交付 queue**。

## 1. 目标

批量、Control API、Scenario、PCM 时间轴、**音频与指令 ACK 契约**、查询接口。

## 2. 范围与非目标

**范围：** Manager、REST+WS、speak_and_wait、wait（必填 device_id）、Scenario、faults+夹具、PCM 时间轴、report 热更新（走同一序号计数器）、音频 ACK、**指令 ACK**、查询 API、reject + cancel_previous。

**非目标：** UI、queue、线上非 pcm、Mongo 预检、用 `sleep_ms:0` 清除节流。

## 3–4. Manager 与并发

同 v5，取消执行架构 §4.5（含 VAD 已写入时保持 `vad`）。  
验收覆盖：并发 CAS；FinishingUpload 在 `vad`/`stage2` 两种已写原因下取消。

## 5. API

同 v5。`POST /devices/{id}/report` 必须使用该实例原子序号，不得另起从 0 的序列。

## 6. Scenario

`tts_done` + idle。skip_register 前 `alloc_fresh_id`。

## 7. 流式发送

内部 PCM + pcm 分帧；Stage=4 先记 `vad` 再补 Stage=2。

## 8. ACK 契约

触发：设备类型 `DownlinkAck=true`，且（音频 `NeedAck=1` **或** 指令 `need_ack=1`）。

### 8.1 配置

```yaml
behavior:
  downlink_ack:
    mode: binary          # binary | json
    sleep_ms: 0           # 写入 ACK 的 SleepMs
    code: 0
```

**`sleep_ms: 0` 语义（写死，对齐 `UpdateMemoryState`）：**

- 出站 ACK 的 SleepMs/memory 均为 0 时，服务端 **跳过写入** 节流缓存。
- **不能**清除该 device 上已有的正值 SleepMs。
- 「不节流」仅当该设备缓存中本就没有正值状态时成立（新设备，或 TTL 过期，默认约 60s）。

因此 0 / binary+正值 / JSON+正值 **必须使用不同 device_id**（夹具新鲜 ID 或未做过正值 ACK 的新实例）。禁止同一设备上「先 500 再 0/换 mode」当作独立对照。

可选：等待 memory TTL 过期后再测 0；不得假设 0 ACK 清状态。

### 8.2 音频 ACK 字段

对刚收到的下行 **音频** 帧：

| binary | 取值 |
|--------|------|
| Ack | 音频头 SequenceNumber |
| DownlinkType | TTS=1；hint=2 |
| Code / SleepMs | 配置 |
| UUID | 28 字节结构无此字段 |

JSON topic：`{ent}/{type}/{id}/downlink-ack/server`  
JSON：`ack`=音频 Seq，`downlink_type`=`tts`/`hint_audio`，`uuid`=音频头 UUID，`sequence_number`=音频 Seq，`sleep_ms`/`code`=配置。

### 8.3 指令 ACK 字段（`need_ack=1`）

对刚收到的 `'1'` 指令（topic 以 `/command/client` 结尾，`data.need_ack=1`）：

| binary | 取值 |
|--------|------|
| Ack | `data.sequence_number`（指令序号；缺省按 0） |
| DownlinkType | **3**（command） |
| Code / SleepMs | 配置 |
| 其余 | 0 |

JSON：

| 字段 | 取值 |
|------|------|
| ack | 指令 `sequence_number` |
| downlink_type | `command` |
| sequence_number | 指令 `sequence_number` |
| topic | **原指令完整 topic**（`.../command/client`） |
| uuid | **省略或 0**（指令无音频 UUID） |
| sleep_ms / code | 配置 |

binary 与 JSON **分别验收**（音频、指令各至少一对）。

### 8.4 节流验收（仅音频 TTS，且每 case 新设备）

设备类型开 DownlinkAck。基线片间隔约 300ms。

| 用例 | 设备 | ACK | 期望 |
|------|------|-----|------|
| A | 新 ID | binary，sleep_ms=0 | 间隔 ≈ 服务端默认片间隔；缓存不被 0 ACK 写入 |
| B | **另一个**新 ID | binary，sleep_ms=500 | 第二片起间隔相对 A **增大约 500ms**（受 6000ms 封顶） |
| C | **再一个**新 ID | json，sleep_ms=500 | 同 B，且出站为 `'1'` downlink-ack |

`frames.jsonl` 核对出站 ACK 内容。  
指令 ACK 验收：收到 `need_ack=1` 后 8.3 字段正确；可用 sleep_ms=0，**不与** B/C 共用设备。

## 9. 故障注入

同架构。`bad_seq` 不断言本地槽空。

## 10. 验收 Checklist

- [ ] 并发 Reserved/409；取消表含 VAD 保持 `vad`
- [ ] 无 queue
- [ ] wait 必填 device_id
- [ ] 音频 ACK：A/B/C 三台设备，无假通过
- [ ] 指令 ACK：binary 与 json 字段符合 8.3
- [ ] `/report` 与 keepalive 序号同一计数器
- [ ] pcm 时间轴；Scenario 按矩阵
