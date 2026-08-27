# Phase 4 按需增强 · 分阶段计划（每阶段一提交）

phase4.md 是按需清单。按"可自治落地、契约可写清"拆成子阶段，逐个实现 + 全量测试 + 提交。

## 阶段与顺序

### 4a speak backlog（Turn 排队）——文档标粗的本阶段主项
- 配置 `behavior.speak_backlog_depth`（默认 0=关闭，Phase 2 允许 0..64；Phase 1 CLI 配置非零 → 启动拒绝）。
- POST /speak(_and_wait) 槽占用时：depth=0 → 409 现状不变；开启且未满 → 202 `{queued, turn_id, queue_position, instance_id}` + 事件 `speak_queued`；满 → 409 `speak_backlog_full`。
- 排队在 core（DeviceInstance）：turn_id 入队即分配并登记 turnDone（speak_and_wait 可直接 WaitTurn）；uuid/seq_before 出队执行时才有。
- 出队：所有 turn 终态都汇聚 `finishCritical` → `go dispatchBacklog()`。队首 startTurn 失败（permit 不足/不可 speak）→ 事件 `speak_backlog_dropped`（含 reason）+ done ch 发该事件，继续下一项；成功 → 事件 `speak_dequeued` + 正常 turn 流程。
- finalize Phase C：清队，逐项 dropped(reason=finalize)。interrupt 只打断当前 turn，不清队。
- GET /devices/{id} 加 `speak_backlog_len`；OnTurnStarted 回调补 turnRec 的 uuid/seq_before。
- WaitTurn 的 turnTerm 早退放宽到 `speak_backlog_dropped`。

### 4b 静默成功探针
- architecture.md §4.4：正常路径「完全无包的静默成功」与 drop 均为 timeout，不可区分；探针属 Phase 4。
- 配置 `behavior.silence_probe`（默认 false）。turn 以 `turn_end_reason=timeout` 且全程无下行包终态后，异步发一次探针 report（复用 pending_reports echo 机制）：echo 正常 → 事件 `silence_probe`（probe=echo_ok，倾向静默成功）；echo 超时 → probe=echo_timeout（倾向被 drop）。不改终态语义、不动完成矩阵。

### 4c HTTP 上传 raw PCM（须带完整 fmt）
- POST /assets 现只收 WAV。增加 raw PCM：multipart 带 `sample_rate`/`channels`/`sample_format` 三项全给才收（缺一 → 400），服务端包 WAV 头后落盘，其余（时长/大小上限、epoch）走现有资产管线。

### 4d 跨设备全局事件总线
- 现 event_seq 是 instance 级。新增 manager 级 `global_seq`：appendEvent 后镜像投递 manager 总线（环形，容量沿用 event_log_max_entries）。
- `GET /ws/events/global?after_global_seq=`：回放 + live，条目带 device_id/instance_id/global_seq。锁序：bus 锁为叶子，禁止回调设备锁。
- 慢订阅：inbox 超限 abort（沿用 wsHub 语义的简化版）。

### 4e 断开即 interrupt（默认关）
- 配置 `behavior.interrupt_on_disconnect`（默认 false）。开启时事件 WS 订阅 abort（客户端断开/半开）→ 对该设备当前 turn 走 CancelTurn（不 BeginClose）。drain（device_deleted）不触发。

### 4f 线上 mp3/wav 推流（评估后再做/不做）
- wav：整段 PCM 一次编码（加头）再按 slice 切流可行。mp3 需编码器依赖（纯 Go shine 质量/维护存疑），默认不引入。做 wav、mp3 留待有真实需求。

## 明确不做（本轮）
- Mongo 预检 / 清 Redis deviceMemory：外部服务不可用，属线上运维钩子。
- 浏览器麦克风、指标、SQLite、压测、MQTT：文档存目级，无契约细节，各自独立大项。
- wake 交叉、dup_uuid：文档要求「经基线实证后再写入 drop 矩阵」，需真实基线服务器。
- 换树重写 bad_seq：条件（换 ai-creates-wealth 树）不成立。

每阶段完成：更新 phase4.md 契约 + README 落地行 + 全量 go test + gofmt + 独立提交。
