# Phase 4 按需增强

阶段边界不变。不强制。本阶段才引入 **speak backlog**（Turn 排队），与 writePump **outbound buffer** 不是同一功能。CancelTurn / BeginClose 属于运输层，不属于本阶段。

## 已落地

### speak backlog（Turn 排队）

- 配置 `behavior.speak_backlog_depth`（0..64，默认 0=关闭）。Phase 1 CLI 配置非零 → 启动拒绝。PUT 归 behavior 组（Created/Stopped 200，Running 409）。
- POST `/speak`(`_and_wait`) 槽占用时：关闭 → 409 现状不变；开启且未满 → 202 `{queued:true, turn_id, queue_position, instance_id}`（无 `uplink_uuid`/`seq_before`，出队执行时补进 turn 记录）；满 → 409 `speak_backlog_full`。
- 排队在 core（DeviceInstance，`deviceMu` 保护）：turn_id 入队即分配并登记 done ch，`speak_and_wait`/`WaitTurn` 直接等待。事件：入队 `speak_queued`（reason=pos=N）、出队 `speak_dequeued`、作废 `speak_backlog_dropped`（reason ∈ finalize/not_speakable/speak_permit/error）。
- 出队：一切 turn 终态汇聚 `finishCritical` → 异步 dispatch。队首启动失败（permit 不足/不可 speak）记 dropped 并继续下一项；成功启动即停，等下一次终态。
- 收口（stop/断链/删除）Phase C 清队，逐项 dropped(finalize) 并唤醒 waiter；`WaitTurn` 对 dropped 与 turn_terminal 同样收梢（speak_and_wait 200，`event_type=speak_backlog_dropped`）。interrupt 只打断当前 turn，不清队。
- `GET /devices/{id}` 增加 `speak_backlog_len`。

## 按需清单（未落地）

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
