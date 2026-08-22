# Phase 4 按需增强（v13）

阶段边界不变。不强制。进入排期前补范围与验收。

- 静默成功探针（无相关下行 vs drop）
- queue：长度、入队超时、取消表（与 writePump 有界队列不是同一功能）
- 线上 mp3/wav：整段 PCM 只编码一次再切流
- 可选 Mongo 预检 / 清 Redis `deviceMemory:{deviceID}`
- 断开即 interrupt（默认关）
- 不查 DownlinkAck；不提供 device_id 重键
- 浏览器麦克风、指标、回放、SQLite、压测、MQTT
- wake 交叉场景
- `dup_uuid` / 错误 Stage 经基线实证后再写入 drop 矩阵
- 换其它 `ai-creates-wealth` 树时重写 `bad_seq` 与对齐基线
