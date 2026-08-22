# 玩具设备模拟器规划审查（v23）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v23/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`；Gorilla `github.com/gorilla/websocket v1.5.3`  
本文不改 v23 设计正文。修订见 `docs/toy-device-simulator-docs-v24/`。

**决策：NEEDS_CHANGES**

迭代原则不变。阶段边界未破。v22 的 `requestClose` 锁所有权 P1 已关闭。

---

## 1. 阻塞

### B1. 空闲 Ping 的超时在 Gorilla v1.5.3 下无效

`architecture.md` 要求解锁后 `SetWriteDeadline(idle_deadline)` 再 Ping。Ping 被规定为 `WriteControl`。该库 `WriteControl(messageType, data, deadline)`：

- 参数 `deadline` 为零时，抢写锁等待退化为 **1000 小时**，随后 `SetWriteDeadline(零值)`（无期限写）。
- 非零时用该参数覆盖 conn 写 deadline，调用前的 `SetWriteDeadline` **无效**。
- `deadline` 已过期则立即 `errWriteTimeout`。

照字面实现 `SetWriteDeadline` + `WriteControl(Ping, nil, time.Time{})`，writer 可永久挂在 Ping 上，到不了 `idle_deadline` 的 `finishAbort()`。Close 只有 writer 能做，连接与协程泄漏。「进入空闲到半开 abort ≤ T」失效。

**改为：** `Ping = WriteControl(PingMessage, nil, idle_deadline)`。禁止用 `SetWriteDeadline` 约束 Ping；禁止传零值 deadline；deadline 已过期视为 Ping 失败 → `abort()`。`WriteMessage` 仍用 `SetWriteDeadline(now+T)`。

---

## 2. 非阻塞

- v22 P1 已关闭：`requestCloseLocked` / `requestClose` / `abort` / `finishAbort` 锁所有权唯一；idle 为锁内判定、解锁后 IO；无「已持锁则…」双入口。
- 事件 WS 仍禁止 `SetReadDeadline`；一读一写仍成立。
- 无新产品功能、无阶段边界移动、未引用旧版本目录。
- 小瑕：T/2 宜写明一次性 timer；`ping_at` / `idle_*` 宜写明 writer 私有；升级失败禁止调 `finishAbort`。

---

## 3. 验证缺口

无实现。须覆盖：T/2 `WriteControl` 带 `idle_deadline`、TCP 黑洞 / 不回 Pong 时 writer ≤ T 退出且 socket 已关、锁内无 IO、无二次加锁。冒烟只对声明的基线提交。
