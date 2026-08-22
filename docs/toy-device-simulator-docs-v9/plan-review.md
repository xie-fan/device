# 玩具设备模拟器规划审查（v9）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v9/`  
对照依据：v9 正文；基线 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
（`module/register/register.go` L226–232：Redis `SetNX` 出错直接 `return`，不发 register ACK）  
审查对象：v9 实施计划。本文不改 v9 设计正文。修订见 `docs/toy-device-simulator-docs-v10/`。

**决策：NEEDS_CHANGES**

---

## 1. 结论摘要

v8 的三条 P1 已正面处理。本轮 **4 条 P1 + 1 条 P2**：report 取号非原子；Starting 在无 ACK 时仍可能永不收口；interim ASR 可被当成 silent 成功；上行未结束时终态下行会释放槽；Phase 2 丢掉批量/错峰/限额契约。

---

## 2. 阻塞问题

### B1. report 取号失去原子性

**位置：** `architecture.md` L258–267。

修正了首值，却推荐无锁「读取后递增」。keepalive 与 `POST /report` 可并发，可能取得相同序号并覆盖 `pending_reports`。

**应改为：** 同一把锁内完成「取号、递增、登记 pending」；或 `atomic.Add` 且内部初值为 `start-1`，再用锁/`sync.Map` 插入 pending。禁止无锁读改写。发送可在登记之后、锁外进行。

### B2. `fail_connection` 不能保证 Starting 必然收口

**位置：** `architecture.md` §4.11；无 `register_ack_timeout`；非零 register ACK 未规定进入 `fail_connection`。

基线 Redis 出错可不发 ACK。实例可永久停在 Starting/Registering。

**应改为：** `register_ack_timeout`；`code != 0` 发 `ack_failure` 后走 `fail_connection` → Stopped。自动化验收：无 ACK 超时、拒绝码，均可再次 start。

### B3. interim ASR 误判为静默成功

**位置：** `architecture.md` L164、L177。

矩阵只「建议」IsFinal，却允许任意匹配 `asr_result` 进入 silent/idle。中间结果只说明处理未完成。

**应改为：** 仅 `IsFinal=true` 可启动 `post_final_asr_silence`。仅有 interim → 按 timeout / drop，不得 `idle`。

### B4. 终态下行可能在上行未结束时释放 Turn

**位置：** `architecture.md` L143、L163。

Speaking/FinishingUpload 的 command 可归入 Turn 并立即启动 followup，未等 WaitingReply。长音频或无 UUID 的外部 command 可能使槽释放后，旧协程仍发 Stage=1。

**应改为：** 上行收口前进终态下行只缓存、不启动完成计时、不 Terminal。进入 WaitingReply 后再按矩阵计时。失败 JSON 可立即停上行。发送协程每帧检查 Terminal。

### B5.（P2）Phase 2 丢失批量交付契约

**位置：** `architecture.md` 仍写批量/模板/错峰；`phase2.md` 无批量创建/启停 API、模板 ID、stagger、`max_connections`、`max_concurrent_speaking` 及验收。

**应改为：** 写回独立可实施的批量 API、限额与验收项。

---

## 3. 非阻塞

- `fail_connection` 应先写入 `connection_failed` 事件日志，再唤醒 waiter。
- 恢复 JSON 指令 ACK 完整字段表；A/B/C 前置改为同一 `device_type`（不是同一 ACK mode）。

---

## 4. 验证缺口

- 规划文档，无模拟器可跑功能测试。
- 基线 SHA 无漂移；审核时六份 v9 设计文档未改。
