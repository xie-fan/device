# 玩具设备 WebSocket 交互协议

本文只描述 **chatbot 玩具设备** 从建连到收下行音频的真实协议，依据 `ai-creates-wealth` 当前实现，不包含模拟器设计。

范围：

- 建连握手
- 设备注册
- 上报对话模式
- 上行录音
- 下行 TTS / 提示音
- 这一路上会碰到的管理 JSON、ASR 文本、指令、ACK、打断、VAD

不包含：babycare/ipc 的 JSON frame、MQTT 独立订阅进程的订阅清单、拍照 `'2'`、转发 `'3'`。MQTT 与 WebSocket **音频头相同**，差异只在传输封装，文末附录说明。

对应服务入口：`docker/ai-chat-bot-distributed/core/main.go`  
本地默认：`ws://127.0.0.1:8089`（`config.yaml` 的 `app.webSocketPort`）

---

## 1. 一次完整对话时序

按键模式（playMode=1）最小闭环：

```text
设备                                          core (WS :8089)
 |                                                |
 |  GET Upgrade  Header Device / Action           |
 |----------------------------------------------->|
 |  101 Switching Protocols                       |
 |<-----------------------------------------------|
 |                                                |
 |  '1' + register JSON                           |
 |----------------------------------------------->|
 |  '1' + register ACK                            |
 |<-----------------------------------------------|
 |                                                |
 |  '1' + report JSON (playingMode=1)             |
 |----------------------------------------------->|
 |  '1' + report 回显                             |
 |<-----------------------------------------------|
 |                                                |
 |  '0' + AudioHeader(Stage=1,Seq=0..n) + 分片    |
 |----------------------------------------------->|  ASR
 |  '0' + AudioHeader(Stage=2,Seq=n+1) [可空载荷] |
 |----------------------------------------------->|  ASR final → LLM/指令 → TTS
 |                                                |
 |  (可选) 无前缀 JSON  asr_result                 |
 |<-----------------------------------------------|
 |  (可选) '0' Stage=4 VAD 通知                    |
 |<-----------------------------------------------|
 |  '0' + AudioHeader(Stage=1,Seq=0..k) + TTS分片 |
 |<-----------------------------------------------|
 |  (可选) '1' + command JSON                      |
 |<-----------------------------------------------|
 |  (可选) '4' 或 downlink-ack                     |
 |----------------------------------------------->|
```

连续对话 / 唤醒（playMode=2/3）差别：服务端用 VAD 判停，可能先下发 `Stage=4`，设备应停录并补 `Stage=2`。

---

## 2. 建立连接

### 2.1 URL

| 环境 | URL | 说明 |
|---|---|---|
| 本地 core | `ws://127.0.0.1:8089/` | `http.HandleFunc("/")`，path 不参与设备识别 |
| 线上 ALB | `ws(s)://{host}/{enterprise}` | path 常为厂商名，给网关路由；设备身份仍看 Header |

WebSocket opcode：服务端 **下行一律 `TextMessage`**，payload 可以是二进制音频。上行 `TextMessage` / `BinaryMessage` 都能被读成 `[]byte`。

### 2.2 握手参数

优先读 HTTP Header，缺了再读 query。`chatbot` **无鉴权**。

| 位置 | 名 | 必填 | 值 |
|---|---|---|---|
| Header 优先 | `Device` | 是 | `{enterprise}/{deviceType}/{deviceID}`，恰好 3 段，`/` 分隔 |
| Header 优先 | `Action` | 是 | 玩具固定 `chatbot` |
| Query 兜底 | `device` | Header 空时 | 同上 |
| Query 兜底 | `action` | Header 空时 | 同上 |

`Device` 三段分别成为连接上的：

- `Enterprise`
- `DeviceType`
- `DeviceID`（同时作为 `client.UserID`）

三段数量不对时解析失败，后续音频/注册会异常。

### 2.3 连接侧限制

| 项 | 默认 | 配置键 |
|---|---|---|
| 读缓冲 | 48 KiB | `app.websocket.readBufferSize` |
| 写缓冲 | 600 KiB | `app.websocket.writeBufferSize` |
| 单帧上限 | 480 KiB | `app.websocket.maxMessageSize` |
| 心跳超时 | 360 秒 | 代码常量 `heartbeatExpirationTime = 6 * 60` |

任意入站消息都会刷新心跳。空闲超过 360 秒会被清掉。没有强制 ping；需要长连就周期发任意合法帧，或依赖业务流量。

连上即标记在线，**还不等于可以说话**。音频入口会查 Mongo：设备必须存在且 `status=1`。

---

## 3. 消息封装

每条 WS 消息第一个字节是类型。

| 首字节 | 常量 | 含义 | 其后内容 |
|---|---|---|---|
| `'0'` `0x30` | `AudioMsg` | 音频 | 100 字节 `AudioHeader` + payload |
| `'1'` `0x31` | `ManageFirstByte` | 管理 JSON | UTF-8 JSON：`{"topic","data"}` |
| `'2'` | 拍照 | 不在本文主路径 | |
| `'3'` | 转发 | 不在本文主路径 | |
| `'4'` | 下行 ACK 二进制 | 28 字节 `DeviceAckMsg` | |
| `'{'` `0x7B` | 无前缀 JSON | 服务端 `ResponseJson` | `{"RequestID","Code","CodeMsg","Data"}` |

服务端解析时会 **剥掉首字节** 再交给业务。设备组帧时必须自己加上。

### 3.1 管理信封

上行、下行管理消息同一形状：

```json
{
  "topic": "{enterprise}/{deviceType}/{deviceID}/{scene}/{target}",
  "data": { }
}
```

完整线上字节：`'1'` + 上面这份 JSON。

Topic 必须 **恰好 5 段**：

```text
{enterprise}/{deviceType}/{deviceID}/{scene}/{server|client}
```

| 段 | 含义 |
|---|---|
| 1 enterprise | 厂商，须在服务端企业配置中存在 |
| 2 deviceType | 设备类型，须有设备类型配置 |
| 3 deviceID | 设备 ID |
| 4 scene | 见下表 |
| 5 target | 设备→云 `server`；云→设备 `client` |

服务端把入站 topic 的最后一段换成 `client` 作为 `ReplayTopic`。

本路径会用到的 scene：

| scene | 方向 | 作用 |
|---|---|---|
| `register` | 上 `server` / 下 `client` | 注册 |
| `report` | 上 `server` / 下 `client` | 上报电量/信号/对话模式 |
| `command` | 下 `client`（也可能上） | 音量、关机、动作、灯、风扇、模式 |
| `downlink-ack` | 上 `server` | JSON 形式 ACK |
| `activate` | 可选 | 激活，不是说话前置条件 |

---

## 4. 设备注册

连上后应先注册。新设备会写入 Mongo，并做 IoT ICCID 校验。已存在设备会更新字段并回 ACK。

同一 `device_id` **每秒只能注册一次**，超限 ACK `code=5001`。

### 4.1 上行

Topic：`{enterprise}/{deviceType}/{deviceID}/register/server`

`data`（`types.RegisterRequest`）：

| JSON 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `sequence_number` | int | 建议 | 设备自增序号，原样语义不强 |
| `enterprise` | string | 是 | 须与握手 Device 第一段一致 |
| `device_type` | string | 是 | 须与握手第二段一致，且已配置 |
| `device_id` | string | 是 | 须与握手第三段一致；空则服务端直接丢弃 |
| `device_imei` | string | 否 | 设备身份 |
| `firmware_version` | string | 否 | 固件版本 |
| `nic_type` | string | 否 | 网卡类型，如 `wifi` |
| `nic_iccid` | string | 新设备实质必填 | 新设备走 `iot.Check(iccid, nicType)` |
| `location` | string | 否 | 位置；也可由 MCC/MNC/TAC/CID 拼 |
| `mcc` | int | 否 | 移动国家码 |
| `mnc` | int | 否 | 移动网络码 |
| `tac` | int | 否 | TAC |
| `cid` | int | 否 | 基站号 |

`location` 为空且 `mcc != 0` 时，服务端拼成 `"mcc,mnc,tac,cid"`。

### 4.2 下行 ACK

Topic：`{enterprise}/{deviceType}/{deviceID}/register/client`

`data`（`types.AckResponse`）：

| JSON 字段 | 类型 | 说明 |
|---|---|---|
| `sequence_number` | int | 当前实现常为 0 |
| `code` | int | **0 表示 IoT/业务成功**。失败为内部错误或 IoT 返回码。限流为 `5001` |
| `message` | string | 说明 |
| `ntp_time` | string | UTC unix 秒，字符串 |
| `expiration` | int | 可选，秒 |

注意：Mongo 里 WebSocket 设备合法时 `status=1`，MQTT 新设备是 `status=0` 再被订阅拉起。**ACK 的 `code` 不是 status**，成功看 `code==0`。

已存在设备：ICCID 变了会重新 `iot.Check`。其它字段变化会更新库。厂商变更会清绑定用户。

---

## 5. 上报对话模式

音频入口用内存/库里的 `playing_mode`。不报的话，库里是 0，按键/VAD 行为会不对。现网脚本（`example/asr/mock.go`）在注册成功后会上报。

### 5.1 上行

Topic：`{enterprise}/{deviceType}/{deviceID}/report/server`

`data`（`types.ReportData`）：

| JSON 字段 | 类型 | 说明 |
|---|---|---|
| `sequence_number` | int | 序号 |
| `code` | int | 设备状态码，可 0 |
| `signalStrength` | int | 信号 |
| `batPowerLevel` | int | 电量 |
| `playingMode` | int | **1 按键 / 2 连续 / 3 唤醒** |

WebSocket 的 report 处理会把 `playingMode` 写入设备表，并 `StoreRegisteredDevicePlayMode`。

### 5.2 下行

Topic：`{enterprise}/{deviceType}/{deviceID}/report/client`

`data` 是入站 `ReportData` 回显，不是 `AckResponse`。

### 5.3 playMode 取值

| 值 | 常量 | 设备行为 | 服务端行为 |
|---|---|---|---|
| 1 | `PlayModePushToTalk` | 按住说，松手发 `Stage=2` | 结束帧立刻收口 ASR |
| 2 | `PlayModeContinuous` | 连续说，等 VAD | 服务端 VAD，可能下发 `Stage=4` |
| 3 | `PlayModeWake` | 唤醒后连续 | 同 2，另有唤醒词打断 `Stage=5` |

音频处理前：`ValidateDevice` 要求设备存在且 `status=1`。不存在则 **静默丢音频**。

---

## 6. 音频头（上行、下行同一结构）

小端、固定 100 字节。Go 结构 `types.AudioHeader`，`encoding/binary.LittleEndian`。

Magic：`Head = 0x4848`。**当前上行解析不校验 magic**，但仍应填写。

### 6.1 字节布局

| 偏移 | 长度 | 字段 | 类型 | 说明 |
|---|---|---|---|---|
| 0 | 4 | `Head` | uint32 LE | `0x4848` |
| 4 | 4 | `Stage` | uint32 LE | 见 6.2 |
| 8 | 4 | `SequenceNumber` | uint32 LE | 同一 UUID 从 0 递增 |
| 12 | 4 | `UUID` | uint32 LE | 本轮会话 ID |
| 16 | 10 | `AudioFormat` | bytes | ASCII，不足补 `0x00`，如 `mp3` `wav` `amr` |
| 26 | 2 | padding | bytes | 必须存在，对齐用 |
| 28 | 4 | `SamplingRate` | uint32 LE | 见 6.4 |
| 32 | 4 | `AudioPayloadLen` | uint32 LE | 后面 payload 字节数 |
| 36 | 4 | `NeedAck` | uint32 LE | 下行：1 表示设备要 ACK；上行填 0 |
| 40 | 60 | `Reserved` | bytes | 全 0 |

合计 100。`binary.Size(AudioHeader)` 必须为 100。

### 6.2 Stage

| 值 | 常量 | 方向 | 含义 |
|---|---|---|---|
| 1 | `AudioStageUploading` | 上/下 | 正在传音频分片 |
| 2 | `AudioStageFinished` | 上（下可能没有） | 本轮录音结束 |
| 3 | `AudioStageBreak` | 上 | 用户打断，停 TTS |
| 4 | `AudioVad` | 下 | VAD 断句，通知设备停录 |
| 5 | `AudioWakeBreak` | 上 | 唤醒词打断 |

下行 TTS 分片当前实现 **Stage 恒为 1**，**不保证**再发 Stage=2 结束帧。收下行应以「一段时间没有新分片」或打断为准，不要死等 Stage=2。

### 6.3 UUID

- 一轮说话一个 UUID，分片共用。
- 取值范围建议 `1 .. 0x7FFFFFFF`。下行 TTS 会把 UUID 当十进制字符串再解析成 uint32；过大或 0 可能组不下发。
- 服务端回放身份：`{deviceID}_{uuid十进制}`。

### 6.4 采样率

设备可填 Hz（`16000`）或 kHz 简写（`16`）。ASR 侧：

- `0` → 按 16000
- `>= 1000` → 原值
- `1..96` → 乘 1000（`16` → 16000）

常量：8000 / 16000 / 24000 / 48000。下行 TTS 头里的采样率来自本轮上行（0 则 16000）。

头注释里的「8 12 16 24 32」是历史简写，实现按上面规则兼容。

### 6.5 格式

`AudioFormat` 前缀匹配（小写、去 NUL）：

`amr` `mp3` `aac` `opus` `wav` `pcm` `speex` `silk` `m4a`

不认识则丢弃该帧。

### 6.6 载荷长度

服务端取 payload 用的是 **帧长 − 100**，不是头里的 `AudioPayloadLen`。头字段仍应填对。单帧 payload **最大 50 KiB**（`MaxBodySize`），超出丢弃。

---

## 7. 上行录音

WebSocket 完整帧：

```text
1 字节 '0'  +  100 字节 AudioHeader  +  payload
```

`ManageHandle` 剥掉 `'0'` 后，`ParseAudioHeader` 读 100 字节，其余当音频。

### 7.1 按键模式推荐时序

1. `Stage=1, SequenceNumber=0`，可带第一片音频（空载荷只会建会话，ASR 没有数据）。
2. 后续 `Stage=1, Seq=1,2,...` 匀速切片。现网脚本按约 100ms 一片。
3. 最后 `Stage=2`，payload 可空，也可把最后一片放在 Stage=2。

约束：

- **新一轮必须从 Seq=0 的 Stage=1 开始**，否则没有活跃 ASR 会话会被丢（设备类型前缀 `MH` 除外，会把非 0 序号当成新一轮）。
- 打断后要换新 UUID，并从 Seq=0 再开始。
- Stage=2 / 3 / 5 之后本轮 turn 结束。

### 7.2 切片大小

没有协议强制。实践：

| 格式 | 100ms 约略 |
|---|---|
| wav/pcm 16kHz 16bit | 3200 B |
| mp3 | ~300 B |
| amr | ~160 B |

下行大音频服务端按 40 KiB（WS）或 20 KiB（MQTT/AAC）切片，间隔 300ms。上行只要每片 ≤ 50 KiB。

### 7.3 连续 / 唤醒

依赖服务端 VAD。可能收到 Stage=4（payload 空，采样率常写 16000）。设备应停止本轮录音并补 Stage=2。

### 7.4 打断

用户抢话或按打断：

- `Stage=3` 普通打断
- `Stage=5` 唤醒词打断

服务端停本轮 TTS。UUID=0 的打断不计入上传限频。

### 7.5 被静默丢弃的情况

没有 WS 错误帧，只打日志：

- 设备不在库或 `status != 1`
- 音频头解析失败
- 格式不支持
- payload > 50 KiB
- Seq>0 且没有活跃 turn（非 MH 机型）

处理失败时可能下发 **无前缀** JSON：

```json
{ "RequestID": "...", "Code": 1, "CodeMsg": "音频处理失败，请稍后重试", "Data": null }
```

用量控制拒绝：`Code=14007`（同类无前缀 JSON）。普通玩具未配厂商用量接口时放行。

---

## 8. 下行音频

WebSocket 完整帧与上行相同：

```text
1 字节 '0'  +  100 字节 AudioHeader  +  payload
```

走 `BuildSliceBytes` → `WriteMessage(TextMessage)`。

### 8.1 头字段（TTS 回复）

| 字段 | 值 |
|---|---|
| `Head` | `0x4848` |
| `Stage` | `1`（正在下发） |
| `SequenceNumber` | 从 0 递增，每个分片 +1 |
| `UUID` | **回声上行 UUID** |
| `AudioFormat` | 本轮格式，常与上行一致，缺省 amr |
| `SamplingRate` | 本轮采样率，0 则 16000 |
| `AudioPayloadLen` | 本分片 payload 长度 |
| `NeedAck` | 设备类型配置了 `DownlinkAck` 则为 1，否则 0 |

提示音（用量拒绝、错误提示）同样是这条音频通道，`NeedAck` 场景下类型记为 `hint_audio`。

### 8.2 分片

| 通道 | 单片上限 | 片间隔 |
|---|---|---|
| WebSocket | 40 KiB | 300 ms |
| MQTT，或 AAC | 20 KiB | 300 ms |

被打断则停止后续分片。

**没有稳定的下行 Stage=2。** 设备侧结束条件：

- 空闲一段时间没有新 `'0'` 帧（现网脚本用 20s idle）
- 收到 Stage=3 类打断（上行自己发的）
- 收到 Stage=4（连续模式停录，不是 TTS 结束）

### 8.3 用 UUID 对齐轮次

下行头 UUID 应等于本轮上行 UUID。其它 UUID 可能是提示音或上一轮残留，应丢弃或单独保存。

---

## 9. 本路径上其它下行

### 9.1 流式 ASR 文本（可选）

仅当设备类型配置 `StreamingAsrTextReply=true`。

**无 `'1'` 前缀**，直接 JSON：

```json
{
  "Action": "asr_result",
  "SessionID": "<uuid 十进制字符串>",
  "Text": "识别文本",
  "IsFinal": false
}
```

`IsFinal=true` 为最终结果。中间帧可多次。

### 9.2 指令

前缀 `'1'`，topic 形如：

```text
{enterprise}/{deviceType}/{deviceID}/command/client
```

`data`（`types.CommandConfig`，字段均可选）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `code` | int | 状态 |
| `message` | string | 文本 |
| `sequence_number` | int | 序号 |
| `setVolume` | int | 音量 0–100 |
| `setTimbre` | string | 音色 |
| `shutDown` | bool | 关机 |
| `playingMode` | int | 切模式 1/2/3 |
| `total` | int | 动作条数等 |
| `light` | int | 灯 |
| `fan` | int | 1 开 2 关 |
| `movements` | array | 多动作 |
| `movement` | object | 单动作：`behavior` `angle` `distance` `start_text` `end_text` `start_voice` `end_voice` |
| `data` | any | 扩展 |
| `need_ack` | int | 1 要求 ACK |

设备执行指令与播放 TTS 是独立通道；长音频不应堵住指令。

### 9.3 VAD 通知

`'0'` + 头，Stage=4，Seq=0，payload 空，UUID=本轮，格式与当前会话相同，采样率常 16000。

### 9.4 通用错误 JSON

无前缀：

```json
{
  "RequestID": "<日志请求 ID>",
  "Code": 1,
  "CodeMsg": "...",
  "Data": null
}
```

`Code=0` 成功。`Code=1` 失败。`Code=14007` 用量拒绝。

---

## 10. 设备 ACK（可选）

仅设备类型配置 `DownlinkAck=true` 时，下行音频头 `NeedAck=1`，指令 JSON 带 `need_ack=1`。未配置可忽略。

### 10.1 二进制（推荐）

`'4'` + 28 字节小端：

| 偏移 | 字段 | 类型 | 说明 |
|---|---|---|---|
| 0 | `Ack` | uint32 | 确认序号 |
| 4 | `DownlinkType` | uint32 | 0 unknown / 1 tts / 2 hint_audio / 3 command / 4 manage / 5 sync_text / 6 activate / 7 device_settings / 8 upgrade |
| 8 | `Code` | uint32 | 0 或 200 成功 |
| 12 | `MemoryPercent` | uint32 | 内存占用，0 表示不上报 |
| 16 | `SleepMs` | uint32 | 建议服务端节流毫秒 |
| 20 | `MemoryTotalKB` | uint32 | 总内存 |
| 24 | `MemoryFreeKB` | uint32 | 空闲内存 |

`SleepMs` 会影响后续下发间隔（速度控制默认开）。

### 10.2 JSON

Topic：`{enterprise}/{deviceType}/{deviceID}/downlink-ack/server`

```json
{
  "ack": 1,
  "downlink_type": "tts",
  "topic": "...",
  "uuid": 123,
  "sequence_number": 0,
  "code": 0,
  "message": "",
  "memory_percent": 0,
  "sleep_ms": 0,
  "memory_total_kb": 0,
  "memory_free_kb": 0
}
```

`downlink_type` 字符串：`tts` `hint_audio` `command` `manage` `sync_text` `activate` `device_settings` `upgrade` `unknown`。

---

## 11. 设备最小实现步骤

1. `Dial`：`Device={ent}/{type}/{id}`，`Action=chatbot`。
2. 发 register，等 `'1'` 且 topic 以 `/register/client` 结尾，`data.code==0`。
3. 发 report，`playingMode=1`，等 `/report/client`。
4. 选 UUID∈[1, 0x7FFFFFFF]，按格式切片发 `'0'` 帧，Seq 从 0 加，最后 Stage=2。
5. 读循环：
   - `'0'`：若 UUID 匹配则拼接 payload；Stage=4 表示 VAD。
   - `'1'`：按 topic 处理 register/report/command。
   - `'{'`：看 `Code` / `Action=asr_result`。
   - `'4'`：若开启 ACK。
6. 下行音频以 idle 超时结束本轮。
7. 需要打断时发 Stage=3，换新 UUID。

---

## 12. 实现时易错点

1. 管理消息漏 `'1'`，或音频漏 `'0'`。
2. `AudioHeader` 少写 2 字节 padding，头会错位到 98 字节。
3. 用 `int32` 生成 UUID 导致负数。
4. 新一轮 Seq 不从 0 开始，音频被丢。
5. 未 register / `status!=1`，音频静默丢失。
6. 未 report `playingMode`，VAD/收口行为不对。
7. 死等下行 Stage=2。
8. 按 `AudioPayloadLen` 切 payload，而不是「帧长−1−100」（下行组帧两者应一致；上行服务端用帧长−100）。
9. 把无前缀 JSON 当管理信封解析。
10. 握手 `Device` 不是恰好 3 段。

---

## 附录 A. MQTT 差异（同一套头）

MQTT 设备音频头、Stage、注册 JSON 字段相同。差别：

- 没有首字节 `'0'`/`'1'`。音频：topic 上直接 100 字节头 + payload。管理：topic 上直接 JSON `data`。
- 上行 topic：`{ent}/{type}/{id}/upload_voice_slice/server`（以及 finish 等历史 topic）。
- 下行 TTS topic：`{ent}/{type}/{id}/upload_voice_slice/client`。
- 新设备 `status` 先为 0，等订阅进程感知后再变 1。
- 分片上限 20 KiB。

玩具对话主路径是 WebSocket `Action=chatbot`。测 MQTT 需要另启 `mqtt-subscribe` 进程。

---

## 附录 B. 关键代码位置

| 内容 | 位置 |
|---|---|
| 握手 | `websocket/service/init_acc.go` `wsPage` |
| 首字节路由 | `websocket/controller/handle.go` `ManageHandle` |
| 音频头结构 | `common/types/newProtocol.go` `AudioHeader` |
| 头解析 | `mqtt/service/client.go` `ParseAudioHeader` |
| 下行组帧 | `mqtt/service/client.go` `BuildSliceBytes` / `publishV2WithKind` |
| 注册 | `module/register/register.go` |
| 模式上报 | `websocket/controller/report.go` |
| 管理下发 | `common/client/publish.go` `PublishWebsocket` |
| VAD 下行 | `websocket/controller/ws_asr/common.go` `SendVadFlag` |
| ACK | `common/types/deviceAckTypes`，`websocket/controller/downlink_ack.go` |
| 现网一次性客户端 | `example/asr/mock.go` |
