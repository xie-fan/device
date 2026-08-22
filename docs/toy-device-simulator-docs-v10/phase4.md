# Phase 4 按需增强（v10）

- 静默成功探针（无相关下行 vs drop）
- queue：长度、入队超时、取消表
- 线上 mp3/wav：整段只编码一次
- 可选 Mongo 预检 / 清 Redis `deviceMemory:{deviceID}`
- 断开即 interrupt（默认关）
- 不查 DownlinkAck；不提供 device_id 重键
- wake、指标、回放、麦克风、SQLite、压测、MQTT
- dup_uuid / 错误 Stage 经基线实证后再写入 drop 矩阵
- 换其它 ai-creates-wealth 树时重写 bad_seq 与对齐基线
