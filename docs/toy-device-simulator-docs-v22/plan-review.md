# 玩具设备模拟器规划审查（v22）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v22/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v22 设计正文。修订见 `docs/toy-device-simulator-docs-v23/`。

**决策：NEEDS_CHANGES**

迭代原则不变。阶段边界未破。Gorilla 读侧已关闭。

---

## 1. 阻塞

### B1. `requestClose` 锁所有权不唯一，idle timeout 可能自锁

`architecture.md` 允许同一 `requestClose(mode)` 在持锁与未持锁路径调用（「已持锁则禁止重入」靠实现者脑补）。idle timeout 伪代码在持锁下 `requestClose(abort); abort()`，而 `abort()` 再调 `requestClose` 并 Close。Go 非重入 Mutex 会死锁；若隐含解锁则未写出。T/2 Ping 未规定「锁内看 `close_mode`、解锁后才 Ping」。

**改为：** 拆成 `requestCloseLocked(mode)`（调用方必须已持 `device_mu`，禁止 IO）与 `requestClose(mode)`（调用方必须未持锁，内部加锁只调 Locked）。timeout 分支锁内判定并 `requestCloseLocked(abort)`，显式解锁后再丢弃队列、Close。T/2：锁内确认仍 `open` 并记下 `ping_at`，解锁后 Ping。禁止持锁 Ping / Close。

---

## 2. 非阻塞

Gorilla 禁止 `SetReadDeadline`、close_mode、catchup/live、Stage 3 单队列保持。未引用旧版本目录。

---

## 3. 验证缺口

无实现。须用 race detector 钉：T/2 Ping、T timeout、事件到达、drain/abort 竞态；无锁内 IO、无二次加锁。冒烟只对声明的基线提交。
