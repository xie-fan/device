# Phase 4 按需增强（v24）

阶段边界不变。不强制。本阶段才引入 **speak backlog**（Turn 排队），与 writePump **outbound buffer** 不是同一功能。CancelTurn / BeginClose 属于运输层，不属于本阶段。

- 静默成功探针（正常路径 timeout 与 drop 的区分）
- 线上 mp3/wav 推流：整段 PCM 只编码一次再切流
- HTTP 上传 raw PCM（须带完整 fmt；Phase 2 不做）
- 跨设备全局事件总线（当前 event_seq 为 instance 级）
- 可选 Mongo 预检 / 清 Redis deviceMemory
- 断开即 interrupt（默认关；若开启仍走 CancelTurn 而非 BeginClose，除非同时要拆连接）
- 不查 DownlinkAck；不提供 device_id 重键
- 浏览器麦克风、指标、SQLite、压测、MQTT
- wake 交叉；dup_uuid 经基线实证后再写入 drop 矩阵
- 换其它 ai-creates-wealth 树时重写 bad_seq 并对齐新基线提交
