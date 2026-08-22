# 玩具设备模拟器规划审查（v19）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v19/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v19 设计正文。修订见 `docs/toy-device-simulator-docs-v20/`。

**决策：NEEDS_CHANGES**

迭代原则不变：只补契约与正确性；不为阶段加产品功能。  
**文风：** 每个版本目录必须写全正文，禁止引用其它版本目录。同目录文件可以互相参照，但被参照的表必须在本目录写全。

---

## 1. 阻塞

### B1. 锁外 fan-out 会打乱 `event_seq`，backlog → live 也没有原子交接

`appendEventLocked` 只在锁内分配序号并快照通道；各调用方解锁后各自 `wsFanout`，因此 `seq=N+1` 可以先于 `seq=N` 写出。订阅若先拷 backlog 再登记 live，中间事件会丢或重复。

**改为：** instance 级 hub。`appendEventLocked` 在 `device_mu` 内按 `event_seq` 把事件投入每个订阅的 FIFO。每个 WS **一个 writer**（该 socket 唯一 `WriteMessage` 者）。禁止调用方解锁后 fan-out。live 订阅：同一临界区先把 backlog 推进该 FIFO，再登记到 hub，然后才解锁。订阅关闭与投递争同一把 `device_mu`。inbox 满 → 摘掉该订阅，解锁后关 WS；不阻塞 Turn，不把 HTTP `/wait` 改成 504。tombstone WS 不入 hub，单协程写完 backlog 即关。

### B2. Stage 3 控制槽两套准入，且 `stage3_backpressure` 进不了 EventNotify

§4.9 同时写「数据帧 `len==depth-1` 为满、Stage=3 只要 `len<depth`」和「控制槽被其它 uuid 占用或 `len==depth` 则降级」。`==` 与「占用」谓词无法唯一实现。`CancelTurn` 无返回值，降级事件无法并入外层 `acc`。

**改为：** 单一物理队列，长度 `len`，容量 `depth`。`write_queue_depth >= 2`（默认 256，小于 2 启动拒绝 / 400）。准入一律用 **`>=`**：数据帧 `len >= depth-1` 拒绝；Stage=3 `len >= depth` 拒绝。同 uuid 未写出 Stage=3 幂等成功。删除「其它 uuid 占用控制槽」第三条规则。`CancelTurn` 返回 `CancelResult{none,enqueued,backpressure}`。`backpressure` 时调用方仍持 `device_mu`，经 `appendEventLocked` 写入 `local_validation_error`/`stage3_backpressure` 并入 `acc`。

---

## 2. 非阻塞

v18 的两个阻塞点已直线修复。阶段边界未破。未引用旧版本目录。

---

## 3. 验证缺口

无实现、无自动化并发测试。冒烟须在声明的基线提交上做，不要混用户未提交改动。
