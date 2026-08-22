# Phase 4 按需增强（v9）

每项进入排期前单独补范围、依赖和验收。

- **静默成功探针**：`replyText==""` 且设备无相关下行时，接入服务端 turn_finished / 日志，区分 drop 与 silent
- **queue**：最大长度、入队超时、取消表与先写不改
- **线上 mp3/wav**：整段 PCM 只编码一次再切流
- **可选 Mongo 预检** / **清 Redis** `deviceMemory:{deviceID}`
- **断开即 interrupt** 开关（默认关）
- 不查 DownlinkAck；不提供 `device_id` 重键
- wake 交叉、指标、回放、麦克风、SQLite、压测、MQTT
- `dup_uuid` / 错误 Stage 经基线实证后再写入 drop 矩阵
- 换其它 `ai-creates-wealth` 树时重写 `bad_seq` 与对齐基线
