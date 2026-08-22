# Phase 2 详细设计：多设备编排 + API + Scenario（v7）

Turn 占用、取消表、fault 矩阵、ACK 运行时规则见 `architecture.md`。  
并发策略：**默认 reject，可选 cancel_previous，不交付 queue**。

## 1. 目标

在 Phase 1 基础上支持：多设备批量、Control API、高层原语、Scenario、连续 PCM 时间轴、音频与指令 ACK 契约、Phase 3 所需查询接口。

## 2. 范围与非目标

**范围**

- Device Manager（生命周期、模板、错峰、资源限制、每设备 Turn 槽 CAS）
- REST + WebSocket 事件 API
- `speak_and_wait` / `wait_for`（必填 `device_id`）
- Scenario YAML 执行器 + 报告
- 故障注入（矩阵 + 夹具新鲜 ID）
- 内部 PCM 时间轴 + silence
- playMode 热更新（`POST /report`，走同一原子序号）
- 音频 ACK 与指令 ACK（运行时只看报文标志）
- Turn / 帧 / 音频查询、配置读写
- 并发：reject（默认）与 cancel_previous

**非目标**

- Web UI、完整 live mic、上千连接压测
- queue（缺最大长度/取消/超时，推迟 Phase 4）
- 线上非 pcm
- Mongo 预检
- 用 `sleep_ms: 0` 清除节流缓存
- `dup_uuid` / `bad_stage` 的 drop 验收
- 查询服务端 `DownlinkAck` 配置

## 3. Device Manager

- 创建 / 启动 / 停止 / 销毁设备实例
- 从模板批量生成 `device_id`；批量操作 stagger 错峰
- 硬限制：`max_connections`、`max_concurrent_speaking`、per-device 缓冲上限
- 单设备异常 recover，不拖垮进程
- 事件总线按 `device_id` / `turn_id` / `correlation_id` 过滤
- 每设备 Turn 槽 CAS（`architecture.md` §4.1）

## 4. Turn 并发

- 默认 `reject`：槽非空 → **409**，不发帧。
- 可选 `cancel_previous`：按 `architecture.md` §4.6 全表转移（含 Reserved、FinishingUpload、VAD 已写入时保持 `vad`），释放槽后再 CAS 新 Reserved。
- 不交付 `queue`。未实现的 queue 接口应 501 或根本不暴露。

`interrupt` API 遵守同一张取消表。

验收必须覆盖：

- 两个并发 HTTP `speak`：一个 Reserved，一个 409
- FinishingUpload：Stage=2 已发 / 未发
- 已记 `vad` 时取消：`uplink_end_reason` 仍为 `vad`

## 5. API

**设备生命周期**

- `POST /devices` — 创建（支持批量/模板）
- `POST /devices/{id}/start`
- `POST /devices/{id}/stop`
- `DELETE /devices/{id}`
- `GET /devices` / `GET /devices/{id}`

**对话**

- `POST /devices/{id}/speak`
- `POST /devices/{id}/interrupt`
- `POST /devices/{id}/report` — 必须使用该实例原子序号，不得另起从 0 的序列
- `POST /devices/{id}/speak_and_wait` — 受理时即返回/包含 `turn_id`
- `POST /wait` — **必填 `device_id`**，可选 `turn_id`、`event_type`、`timeout`

**Turn 与查询（Phase 3 依赖，本阶段必须交付）**

- `GET /devices/{id}/turns`
- `GET /devices/{id}/turns/{turn_id}`
- `GET /devices/{id}/turns/{turn_id}/frames`
- `GET /devices/{id}/turns/{turn_id}/audio/uplink`
- `GET /devices/{id}/turns/{turn_id}/audio/downlink`

**配置**

- `GET /devices/{id}/config`
- `PUT /devices/{id}/config`

**Scenario**

- `POST /scenarios/run`
- `GET /scenarios/runs/{run_id}`

**故障注入**

- `POST /devices/{id}/faults`
  - 必须在对应生命周期步骤之前配置，或先 stop 再 start
  - `skip_register` 的 ID 来自夹具（请求体覆盖 `device_id` 或先 PUT config）；模拟器仍不查 Mongo
  - 已走过 register 再设 `skip_register` → 400/409
  - 已 Ready 再设 `skip_report` → 400/409
  - 不要在 handler 里写死「必定 drop」

**事件**

- WebSocket `/ws/events` — 过滤含 `device_id`、`turn_id`、`correlation_id`

## 6. Scenario

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

`tts_done`：至少一帧匹配 UUID 的 TTS，且 DownlinkPlayer 因 idle 回到 Idle。零下行不是 `tts_done`。

`skip_register` 步骤前必须有 `action: alloc_fresh_id`；验收读 `fresh_ids.jsonl`。  
`skip_report` 不得 assert `expected_server_drop`。

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

1. 每段 WAV：去掉 RIFF 容器，得到 PCM。
2. 转到内部 PCM：mono、s16le、`sample_rate`。多声道则 downmix 或拒绝。
3. `silence`：在内部 PCM 上生成 `duration_ms` 全零采样。
4. 整条时间轴拼接后按 `slice_ms` 切 **pcm 字节** 成帧发出（`format=pcm`）。
5. 收到 Stage=4：立即记 `vad`（若空），停止后续段，补 Stage=2。

禁止拼接带 RIFF 头的文件字节。上行 `frames.jsonl` 音频 payload 不得出现第二个 RIFF。

## 8. ACK 契约

### 8.0 触发

| 层 | 规则 |
|----|------|
| 模拟器运行时 | 音频 `NeedAck==1` **或** 指令 `need_ack==1` → 必须按 `mode` 发 ACK。标志为 0 → 不发。 |
| 禁止 | 查询或缓存服务端 `deviceType.DownlinkAck` 再决定是否 ACK。 |
| 集成测试 fixture | 需要出现标志的用例：该 `device_type` 在 **core 种子数据** 中 `DownlinkAck=true`，设备已 register 且 `status=1`。收不到标志时判 fixture 失败，不算模拟器漏 ACK。 |

### 8.1 配置

```yaml
behavior:
  downlink_ack:
    mode: binary          # binary | json
    sleep_ms: 0           # 写入 ACK 的 SleepMs
    code: 0
```

- `mode=binary`：发 `'4'` + 28 字节小端。
- `mode=json`：发 `'1'` + topic `{enterprise}/{deviceType}/{deviceID}/downlink-ack/server`。

**`sleep_ms: 0` 语义（对齐基线 `UpdateMemoryState`）：**

- SleepMs 与 MemoryPercent 均为 0 时，服务端 **跳过写入** 节流缓存。
- **不能**清除该 device 上已有的正值 SleepMs。
- 「不节流」仅当该设备缓存中本就没有正值状态时成立（新设备，或 TTL 过期，默认约 60s）。

因此 0 / binary+正值 / JSON+正值必须使用 **不同 device_id**。禁止同一设备上「先 500 再 0 / 换 mode」当作独立对照。不得假设 0 ACK 清状态。

### 8.2 音频 ACK 字段

对刚收到的下行 **音频** 帧：

| binary 字段 | 取值 |
|-------------|------|
| Ack | 该帧 AudioHeader.SequenceNumber |
| DownlinkType | TTS=1；提示音=2 |
| Code | 配置 `code`（默认 0） |
| SleepMs | 配置 `sleep_ms` |
| MemoryPercent / MemoryTotalKB / MemoryFreeKB | 0 |
| UUID | 不在 28 字节结构内 |

JSON：

| 字段 | 取值 |
|------|------|
| ack | 音频 Seq |
| downlink_type | `tts` 或 `hint_audio` |
| uuid | 音频头 UUID |
| sequence_number | 音频 Seq |
| sleep_ms / code | 配置 |
| topic | 可选；音频无管理 topic 时可省略 |

### 8.3 指令 ACK 字段（`need_ack==1`）

对刚收到的 `'1'` 指令（topic 以 `/command/client` 结尾，`data.need_ack==1`）：

| binary 字段 | 取值 |
|-------------|------|
| Ack | `data.sequence_number`（缺省 0） |
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
| uuid | **省略或 0** |
| sleep_ms / code | 配置 |

binary 与 JSON **分别验收**（音频、指令各至少一对）。

### 8.4 节流验收 A/B/C

仅测音频 TTS。三台设备 **全部** 满足：

1. 已走正常路径 register 成功，业务上 `status=1`（不是 skip_register）。
2. **同一** `device_type`，且该类型在 core fixture 中 `DownlinkAck=true`（保证 TTS 头 `NeedAck==1`）。
3. 同一条足够长的 pcm，下行 TTS **至少 2 个分片**（否则无法比较第二片间隔）。
4. 三个 **不同** `device_id`（避免 SleepMs 缓存串台）。

服务端另有约 300ms 片间隔。`speedCtrl` 默认开；SleepMs 封顶默认 6000ms。

| 用例 | ACK | 期望 |
|------|-----|------|
| A | binary，sleep_ms=0 | 片间隔 ≈ 服务端默认；0 ACK 不写入节流缓存 |
| B | binary，sleep_ms=500 | **从第二片起**相对 A 增大约 500ms（不超过 500ms+封顶+片间隔） |
| C | json，sleep_ms=500 | 同 B；出站为 `'1'` + `downlink-ack` |

`frames.jsonl` 核对出站 ACK 内容。

若某台收到 `NeedAck==0`：停止节流断言，报 **fixture**（类型未开 DownlinkAck 或未 status=1），不算模拟器实现失败。

指令 ACK：binary / json 分验；可用 sleep_ms=0；**另用**已注册设备，不与 B/C 共用。

## 9. 故障注入

仅矩阵内 fault。批量 `skip_register`：每个 ID 都来自夹具，验收读 jsonl。  
`dup_uuid` / `bad_stage`：可注入并录帧，不进入 drop 的 pass/fail。  
`bad_seq` 不断言本地槽空。

## 10. 验收 Checklist

- [ ] 可同时启动可配置数量的设备并完成对话；批量错峰
- [ ] 并发 speak：CAS 一个 Reserved、一个 409（reject）
- [ ] cancel_previous 覆盖 Reserved / Speaking / FinishingUpload（Stage=2 已发与未发）/ WaitingReply
- [ ] 已记 `vad` 时取消：`uplink_end_reason` 仍为 `vad`；已 Stage=2 且无 VAD：保持 `stage2`；`turn_end_reason=interrupt`
- [ ] 无 queue API，或明确 501
- [ ] `speak_and_wait` 含受理时即有的 `turn_id` 与 `uplink_uuid`
- [ ] `POST /wait` 无 `device_id` 时拒绝
- [ ] faults 在错误时机返回 4xx
- [ ] Phase 3 所需查询/配置/音频下载 API 全部可用
- [ ] 事件：生命周期用 `correlation_id`，对话用 `turn_id`
- [ ] Scenario：正常路径 `tts_done`+idle；注入按矩阵；skip_register 核对 jsonl
- [ ] silence：内部 PCM 全零分帧；无重复 RIFF
- [ ] Stage=4 先记 vad 再补 Stage=2
- [ ] `NeedAck`/`need_ack` 为 0 时无出站 ACK
- [ ] 模拟器代码路径无 DownlinkAck 配置查询
- [ ] 音频 ACK：A/B/C 满足 §8.4 四条前置；≥2 片 TTS；三台不同 ID
- [ ] 指令 ACK：binary 与 json 字段符合 §8.3
- [ ] `/report` 与 keepalive 序号同一计数器
- [ ] playingMode 可通过 report 热更新
- [ ] 单设备异常不影响其他设备
