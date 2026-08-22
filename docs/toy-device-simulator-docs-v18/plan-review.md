# 玩具设备模拟器规划审查（v18）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v18/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v18 设计正文。修订见 `docs/toy-device-simulator-docs-v19/`。

**决策：NEEDS_CHANGES**

迭代原则不变：只补契约与正确性；不为阶段加产品功能。  
**文风：** 每个版本目录必须写全正文，禁止引用其它版本目录。同目录文件可以互相参照，但被参照的表必须在本目录写全。

---

## 1. 阻塞

### B1. CancelTurn 之后 BeginClose(token=无) 会丢掉 Stage 3

失败 JSON / `/interrupt` 先 `CancelTurn` 把 Stage=3 追加到队尾，并把 Turn 置 Terminal。随后 CLI 退出或 HTTP stop 走 `BeginClose(token=无)`（已 Terminal 不能按取消表再生成 Stage 3），若实现成「清空全部 buffer」则该帧被吞，服务端收不到打断。

**改为：**

1. `BeginClose` 是 **过滤**：丢掉未写出的 Stage=1/2 与 ACK/report/keepalive，**保留**已入队未写出的 Stage=3（每 `uplink_uuid` 至多一帧）。token=无 **不追加** 新 Stage=3，但 Phase B **必须先写出保留的 Stage=3 再关 socket**。
2. token=Stage3：仅当该 uuid 还没有未写出 Stage=3 时才追加；已有则不得第二帧。
3. CancelTurn **先删本 Turn Stage 1/2，再入队 Stage=3**。outbound 深度内为 Stage=3 **保留 1 个控制槽**（数据帧在 `len==depth-1` 视为满）。入不了队 → 不阻塞；Turn 仍 Terminal；`local_validation_error`/`stage3_backpressure`；`/interrupt` 仍 200。

### B2. turn_terminal 未覆盖通用 event waiter 与 WS fan-out

`terminalLocked` 若只返回 completion waiter，live `POST /wait` 登记的 event waiter 会留在表里，即使日志已有 `turn_terminal` 仍可能 504。WS 未来帧也没有锁外 fan-out。

**改为：**

1. 所有事件写入走 **`appendEventLocked`**：append 日志、摘匹配的 live event waiter、快照 WS chan；**不唤醒、不 WriteMessage**。
2. `terminalLocked` 经它写 `turn_terminal`，返回 `TerminalNotify{completion, eventWaiters, event, wsChans}`。
3. 同一临界区内若还写了 `tts_done` 等，调用方必须 **累积** 各次 `EventNotify`，解锁后一并 notify + `wsFanout`。禁止只唤醒 completion。
4. `/wait` 历史检查与登记同一把锁。被摘走的 waiter 超时不得再 504。WS 只在解锁后 fan-out；发送失败只关该 WS。

---

## 2. 非阻塞

v17 的 4 个阻塞点已实质修补。阶段边界未破。未引用旧版本目录。

---

## 3. 验证缺口

无实现、无自动化测试。冒烟须在声明的基线提交上做，不要混用户未提交改动。
