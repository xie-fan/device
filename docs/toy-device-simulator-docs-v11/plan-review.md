# 玩具设备模拟器规划审查（v11）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v11/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v11 设计正文。修订见 `docs/toy-device-simulator-docs-v12/`。

**决策：NEEDS_CHANGES**

阶段边界 **不移动**：P1 仍是单设备 CLI 闭环；P2 仍是批量+API+Scenario；P3 仍是 UI；P4 仍是按需。本轮只补契约与竞态，不把 REST/批量/JSON ACK 提前或后移。

---

## 1. 阻塞问题

### B1. conn_permit 泄漏与重复释放

并发 start 失败方无回滚规则；Stopped / Deleted / fail_connection 多点释放，stop+delete 或 stop+断线可能释放两次。

**改为：** 每次成功 occupy 连接生成 `conn_generation` + `permit_held`。所有退出路径只走 **一个幂等 finalizer**（按 generation 恰好释放一次）。重复 start 在同一把锁里见 Starting/Running → **409 且不 Acquire**。

### B2. `/wait` 丢唤醒窗口

「同锁检查」未规定「检查 Terminal/历史 + 注册 waiter」与终态落库/唤醒在同一临界区。speak_and_wait 的同锁注册不能覆盖 `/wait`。

**改为：** 写死检查 → 注册 →（他方）Terminal 落库 `turn_terminal` → 唤醒 的原子顺序。

### B3. register timer 截止点竞态

先装 timer 再发送已修快速 ACK，但 `Timer.Stop` 不等待已开火的 callback。ACK 与 timeout 同时到达可 Registered 后再被旧 callback `fail_connection`。

**改为：** callback 持 `conn_mu`，校验 generation、waiter id、`Connection==Registering`，与 ACK **一次性消费** register settle。

### B4.（P2）缺少统一 Turn 终态事件

command/JSON/silent/interrupt/timeout 可 Terminal 而无 `tts_done`，事件 WS / UI 无法知道槽已释放。

**改为：** 每次 Terminal 发 `turn_terminal`（`turn_end_reason`、`uplink_end_reason`、`reply_kind`）。先写 event_log 再唤醒。属 **core**（P1 `--wait` 也用），不把 REST 提前。

### B5.（P2）Phase 2 API 契约不足

缺 speak body、`turn_id` 返回、`/wait` 结构、事件 WS 续传、Manager 限额 schema。错误码 410/409、409/429 双答案。

**改为：** 在 **Phase 2 文档**写死请求/响应与错误码表（不改阶段范围）。

---

## 2. 非阻塞

- writePump：队列容量、enqueue 失败、关闭前 drain；**enqueue 前释放 `report_mu`**。
- 验收补：重复并发 start、stop/断线/delete、ACK∩timeout、`/wait` 窗口、所有路径 `turn_terminal`。

---

## 3. 验证缺口

无模拟器实现。未跑功能测试。
