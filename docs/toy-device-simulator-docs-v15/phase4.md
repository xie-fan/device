# Phase 4 按需增强（v15）

阶段边界不变。不强制。

本阶段的 **speak backlog**（Turn 排队、入队超时、取消表）**不是** Phase 1/2 已有的 writePump **outbound buffer**。实现与文档禁止把两者都叫做光秃的「queue」。

其它按需：

- 静默成功探针
- 线上 mp3/wav 推流（整段只编码一次）
- HTTP raw PCM（须带完整 fmt 元数据；Phase 2 不做）
- 跨设备全局事件总线（当前 seq 为 instance 级）
- Mongo 预检 / 清 Redis
- 断开即 interrupt（默认关）
- 不查 DownlinkAck；不提供 device_id 重键（重用 ID 已用 instance_id 隔离，重键仍禁止）
- 麦克风、指标、SQLite、压测、MQTT
- wake；`dup_uuid` 实证后再入 drop 矩阵
- 换其它 `ai-creates-wealth` 树时重写 `bad_seq`
