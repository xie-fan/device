# Phase 4 按需增强（v6）

- queue
- 线上 mp3/wav：整段一次编码再切流
- 可选 Mongo 预检
- 可选调用方清理 Redis `deviceMemory:{id}`（文档化；模拟器默认仍用新设备隔离 ACK 测试）
- wake、指标、回放、麦克风、SQLite、探针、压测、MQTT
- `dup_uuid` 实证后再进矩阵
