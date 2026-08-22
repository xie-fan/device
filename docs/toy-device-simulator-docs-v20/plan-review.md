# 玩具设备模拟器规划审查（v20）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v20/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`

**决策：APPROVED**

迭代原则不变。阶段边界未破。

---

## 1. 已关闭

### 更早轮次

- 禁止解锁后 `wsFanout`；锁内拷贝 backlog；catchup swap 到空才 `live`。
- 单一 outbound 队列；`CancelResult` / `CloseResult`；Phase A `acc`。
- 事件 WS 写出有界：`T=write_drain_timeout_sec>0`。

### 本轮

- **close_mode：** `open|drain|abort`。close inbox 只唤醒且只 close 一次。删除 `drain`（含 `device_deleted`）。客户端 close / 非 deadline 读错误 / 过载 / 写超时 `abort`。abort 覆盖 drain。writer 每次写出前检查。
- **空闲 1T：** 单次写出 `now+T`。空闲窗口固定 `idle_deadline=now+T`，T/2 Ping，Ping/Pong 用剩余时间。读 deadline 到期 **不得** abort。writer 看 `last_pong` 续窗或 abort。离开空闲 `SetReadDeadline(0)`。

---

## 2. 非阻塞

architecture / phase1/2/3 / README 主路径对齐。Pong 成功必须能进入下一窗口。

---

## 3. 验证缺口

无实现。须钉：drain 与 abort 竞态；无 Pong 则从空闲起 ≤T 退出；有 Pong 则 T 之后仍 live；inbox 打断空闲后的写出不被旧读 deadline 拆掉。冒烟只对声明的基线提交。
