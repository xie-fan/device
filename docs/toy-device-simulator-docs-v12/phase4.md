# Phase 4 按需增强（v12）

阶段边界不变。不强制。

- 静默成功探针
- queue 策略（入队超时；与 writePump 有界队列不是同一功能）
- 线上 mp3/wav：整段只编码一次
- 可选 Mongo 预检 / 清 Redis
- 断开即 interrupt（默认关）
- 不查 DownlinkAck；不提供 device_id 重键
- 麦克风、指标、回放、SQLite、压测、MQTT
- wake 交叉；dup_uuid 实证后再入 drop 矩阵
- 换其它 ai-creates-wealth 树时重写 bad_seq
