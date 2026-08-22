# Phase 4 按需增强（v16）

阶段边界不变。不强制。本阶段才有 **speak backlog**（不是 writePump outbound buffer）。

- 静默成功探针
- 线上 mp3/wav 推流
- HTTP raw PCM（Phase 2 不做）
- 跨设备全局事件总线
- Mongo / Redis 清理
- 断开即 interrupt（默认关）
- 不查 DownlinkAck；不提供 device_id 重键
- 麦克风、指标、SQLite、压测、MQTT
- wake；`dup_uuid` 实证后再入 drop 矩阵
- 换其它 ai-creates-wealth 树时重写 `bad_seq`
