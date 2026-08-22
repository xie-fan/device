# Phase 4 按需增强（v5）

- queue（最大长度、入队超时、取消表）
- 线上 mp3/wav：整段 PCM **一次**编码再切流；禁止逐片封装
- 可选 Mongo/core 查询预检（skip_register 的补充证明，不替代夹具）
- wake 交叉场景、指标、回放、麦克风、SQLite、探针、压测、MQTT
- `dup_uuid` 基线实证后再进 drop 矩阵
