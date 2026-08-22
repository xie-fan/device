# Phase 4 按需增强（v8）

本阶段不强制。每项进入排期前单独补范围、依赖和验收标准。

- **queue 并发策略**：须定义最大长度、入队超时、取消时 Stage=3 与 `uplink_end_reason` 先写不改规则
- **线上 mp3/wav**：整段内部 PCM **只跑一次编码器**，再对编码流分帧；禁止逐片封装容器
- **可选 Mongo / core 查询预检**：作为 skip_register 的补充证明，不替代夹具 jsonl，也不做成模拟器默认运行时依赖
- **可选清理 Redis** `deviceMemory:{deviceID}`：文档化给测试套件；模拟器默认仍用新设备隔离 ACK 节流用例
- **调用方断开即 interrupt**：若产品需要与当前「只取消 waiter」不同，单独开关，默认关闭
- 不把「查询 DownlinkAck」做成模拟器运行时依赖
- 不提供 `device_id` 重键 API
- 更完整的 wake 词打断与交叉场景
- 指标面板（连接数、进行中 Turn、错误分布、关键路径延迟）
- 会话回放对比（两次运行帧/结果 diff）
- 浏览器实时麦克风完整链路
- SQLite 会话历史查询与过滤
- `server_observed_drop` 探针（接入服务端日志）
- 高并发压测优化
- MQTT 路径对照（非主路径）
- `dup_uuid` / 错误 Stage 经基线实证后再写入 drop 矩阵
- 切换到其它 `ai-creates-wealth` 树时，重写 `bad_seq` 与对齐基线
