# 玩具设备模拟器规划审查（v24）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v24/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`；Gorilla `github.com/gorilla/websocket v1.5.3`  
本文不改 v24 设计正文。正式实施文档见 `docs/toy-device-simulator/`。

**决策：APPROVED**

迭代原则不变。阶段边界未破。

---

## 1. 已关闭

- v22 P1：`requestCloseLocked` / `requestClose` / `abort` / `finishAbort` 锁所有权唯一；idle 锁内判定、解锁后 IO。
- v23 P1：Ping = `WriteControl(PingMessage, nil, idle_deadline)`。禁止用 `SetWriteDeadline` 约束 Ping；禁止零值 deadline。
- 事件 WS 禁止 `SetReadDeadline`；一读一写；`close_mode`；catchup 空且 `open` 才 live；Stage 3 单队列。

---

## 2. 验证缺口

无实现。实施时须用 race detector 与 Gorilla v1.5.3 钉：T/2 Ping 有界、T timeout、drain/abort 竞态、无锁内 IO、无二次加锁。冒烟只对声明的基线提交。
