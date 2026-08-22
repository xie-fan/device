# 玩具设备模拟器规划审查（v21）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v21/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`（Gorilla websocket v1.5.3）

**决策：APPROVED**

迭代原则不变。阶段边界未破。

---

## 1. 已关闭

- 事件 WS **禁止** `SetReadDeadline`。空闲半开用独立 timer：T/2 Ping，到 T 由 writer 比较 `last_pong` 与 `ping_at`；无 Pong 则 abort 并 `Close` 唤醒 reader。Pong 经 reader 同协程启动前的 `SetPongHandler`。总预算仍 ≤ T。避开 Gorilla 读侧并发与 timeout 后读状态损坏。
- `close_mode=open|drain|abort`、catchup 空才 live、单一 Stage 3 队列、`CancelResult` / `CloseResult` 维持。

---

## 2. 非阻塞

architecture / phase2 / README 对齐。Ping 后仍在同一 `idle_deadline` 窗口内等待再判定。

---

## 3. 验证缺口

无实现。须用 Gorilla v1.5.3 钉：有 Pong 跨多个 T 仍 live；无 Pong 从空闲起 ≤T 退出；abort 的 Close 能打断 reader；事件 WS 路径无 `SetReadDeadline`。冒烟只对声明的基线提交。
