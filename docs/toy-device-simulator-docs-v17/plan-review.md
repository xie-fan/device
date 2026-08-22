# 玩具设备模拟器规划审查（v17）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v17/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v17 设计正文。修订见 `docs/toy-device-simulator-docs-v18/`。

**决策：NEEDS_CHANGES**

迭代原则不变：只补契约与正确性；不为阶段加产品功能。  
**文风：** 每个版本目录必须写全正文，禁止引用其它版本目录。同目录文件可以互相参照，但被参照的表必须在本目录写全。

---

## 1. 阻塞

### B1. Turn 级 interrupt 没有可实现的泵操作

`/interrupt` 与失败 JSON 要停本 Turn 并按取消表发 Stage 3，但文档里唯一会清待发 Stage 1/2 的是 `BeginClose`，它会 `closing=true` 并结束连接。协议里 Stage 3 只打断本轮语音，连接继续复用。

**改为：** 定义与 `BeginClose` 并列的原子 `CancelTurn`：只删除本 `turn_id` 未写出的 Stage 1/2，按取消表追加 Stage 3，**不** 置 `closing`、**不** 清空 ACK/report。`BeginClose` 仍只用于关连接。补全 `/interrupt`：必填 live `instance_id`；无活动 Turn → 200 `interrupted:false`；成功则在 `turn_terminal` 之后 200；`finalize_started` → 409。失败 JSON 走 `CancelTurn`，不走 `BeginClose`。

### B2. Phase C 与通用 Terminal 的锁 / waiter 重叠

§4.1 Terminal 自行加 `device_mu`、复制 waiter 并解锁唤醒；Phase C 已持锁却「走 §4.1」，随后又取 Turn waiter。直接调用会重复加锁，内联又可能重复摘取或唤醒。

**改为：** 唯一锁内迁移函数 `terminalLocked`：调用方必须已持 `device_mu`；本函数不再加锁、不做 IO、不唤醒；写快照、释槽、append `turn_terminal`、摘 waiter 并返回。外层解锁后统一通知。Phase C 只调用它一次，禁止再取一遍 Turn waiter。

### B3. tombstone 省略游标语义冲突

架构写省略 `after_event_seq` 只等未来；phase2 又要求 tombstone WS 回放到 `device_deleted` 后关闭。冻结日志没有未来事件。

**改为：** 省略游标一律视为 `after_event_seq=0`（从 oldest 起）。该规则同时约束 GET events、`/wait`、WS。tombstone 为冻结日志：命中则回放到含 `device_deleted`；WS 随后关闭；`/wait` 未命中 → 404，不 504。只要未来事件必须显式传入当前 `newest_seq`。

### B4. Turn / 录音查询没有实例世系

`device_id` 删除后可重用，磁盘路径含 `instance_id`，但 turns/frames/audio 只收 `device_id`/`turn_id`。重建后旧录音可能 404、串到新实例，或读到错误世系。

**改为：** 这些接口必填 `instance_id`（查询参数），路由与 events 相同：精确命中 live 或 TTL 内 tombstone。禁止只凭 `device_id`+`turn_id` 猜测。缺 `instance_id` → 400。重建后用新 `instance_id` 查旧 `turn_id` → 404。

---

## 2. 非阻塞

v16 的 6 个阻塞点已实质修复。阶段边界未破。冒烟须在声明的基线提交上做，不要混用户未提交改动。

---

## 3. 验证缺口

无实现、无自动化测试。
