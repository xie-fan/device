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

### 静默成功探针

- 配置 `behavior.silence_probe`（bool，默认 false=关闭）。Phase 1 CLI 配置 true → 启动拒绝。PUT 归 behavior 组。
- 触发：turn 以 `turn_end_reason=timeout` 且全程无任何 turn 下行（无 interim/TTS/命令/JSON/is_final）终态后，异步发一次探针 report（复用 pending_reports echo 机制；仅 Ready 且未 finalize 时发，不占 turn 槽）。
- 结果事件 `silence_probe`（turn_id=原 turn）：echo 正常 → reason=`echo_ok`（链路仍活，倾向服务端「静默成功」）；echo 超时 → reason=`echo_timeout`（倾向上行被 drop）。伴随常规 `report_echo`/`report_timeout` 事件。
- 不改终态语义、不动完成矩阵；有过任何下行包的 timeout（如仅 interim）不触发。

### HTTP 上传 raw PCM

- `POST /assets` multipart 携带 `sample_rate`/`channels`/`sample_format` 三项全给才按 raw PCM 收；只给一部分 → 400；全不给 → 现有 WAV 路径不变。
- 校验：`sample_format` 仅 `s16le`；`sample_rate`/`channels` 正整数；内容不得以 RIFF 开头（防误传 WAV）；长度须为帧大小（channels×2 字节）整数倍。
- 服务端 `EncodeWAV` 包头后与普通上传走同一管线（`max_asset_bytes`、`max_asset_duration_sec`、epoch、speak 时设备 audio 指纹校验）。响应 `container=wav`。

### 跨设备全局事件总线

- manager 级总线：各设备 EventLog 追加事件时镜像投递（`SetMirror`，回调在 EventLog 临界区内、总线锁是叶子锁，禁止回调设备锁），统一分配 `global_seq`（严格递增）。环形容量沿用 `event_log_max_entries`。
- `GET /ws/events/global?after_global_seq=N`：回放 + live，条目为事件 wire 格式外加 `global_seq`（`device_id`/`instance_id` 事件本身已带）。`after < evicted_through` → 410 `global_seq_expired`；参数非法 → 400。
- 慢订阅：inbox（256）满即摘除并关连接（abort），不阻塞事件产生方。instance 级 `event_seq`/`GET /ws/events` 语义不变。

### 断开即 interrupt

- 配置 `behavior.interrupt_on_disconnect`（bool，默认 false=关闭）。Phase 1 CLI 配置 true → 启动拒绝。PUT 归 behavior 组。
- 触发：该设备的事件 WS 订阅进入 abort（客户端断开、写失败、半开 ping 无 pong、慢订阅被掐）时，对当前 turn 走 CancelTurn（`turn_end_reason=interrupt`）；槽空或 finalize 中为 no-op。连接不拆（不 BeginClose）。
- drain 不触发：设备删除（`device_deleted` → WSDrain 收口）不算断开。回调经 EventLog 异步触发，不在锁内。

## 按需清单（未落地）

- 线上 mp3/wav 推流：整段 PCM 只编码一次再切流
- 可选 Mongo 预检 / 清 Redis deviceMemory
- 不查 DownlinkAck；不提供 device_id 重键
- 浏览器麦克风、指标、SQLite、压测、MQTT
- wake 交叉；dup_uuid 经基线实证后再写入 drop 矩阵
- 换其它 ai-creates-wealth 树时重写 bad_seq 并对齐新基线提交
