# 玩具设备模拟器规划审查（v10）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v10/`  
对照：v10 正文；最初 `docs/toy-device-simulator-docs/` 产品目标与硬约束；基线 `5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v10 设计正文。修订见 `docs/toy-device-simulator-docs-v11/`。

**决策：NEEDS_CHANGES**

本轮有两份方向不同的审查。v11 **两份都吸收**，不把产品退回 v1，也不把 Phase 1 做成纯竞态附录。

---

## 0. 两份审查如何合成

| 来源 | 要求 | v11 处理 |
|------|------|----------|
| 协议正确性 | register 先装 timer 再发送；出站串行化；限额原子闸门；fault 的 Starting→Running；正文独立可读 | 全部作为约束写入，不回退 |
| 产品目标 | Phase 1 仍是可交付闭环；写回 keepalive/360s、读循环不阻塞、playingMode 热更新；§10 分成交付验收与正确性附录 | 恢复为硬约束与交付验收；竞态条款进附录，不替换主路径 |

**明确不退回：** Go、按字节收 TEXT、mock.go 退出 golden、one-shot CLI、fault 矩阵、Turn 拆分、提前下行缓存、report 锁内取号、register 超时收口、仅 IsFinal silent、device_id 不可变、ACK 只看报文、线上 pcm、批量/模板/错峰/限额。

---

## 1. 协议侧阻塞（P1）

### B1. register timer 先发送后登记

「已发 register 后启动」会在立即 ACK 时误杀健康连接。必须锁内 `Registering` + 登记 timer/waiter，**再**出站；任意 ACK 锁内取消 timer。

### B2. 缺少连接级出站串行化

音频 / report / keepalive / ACK / interrupt 可并发写同一 WebSocket。需要 `writePump` 或 `write_mu`；Stage=1/2 保序；写失败 → `fail_connection`。

### B3. 全局限额非原子闸门

并发 start/speak 可同时检查通过。需要 permit/semaphore：获取、失败回滚、Stopped/Terminal/`fail_connection` **恰好释放一次**，加并发超限测试。

### B4. fault 无 Starting→Running

`skip_register` 停 Connected、`skip_report` 停 Registered，speak 又要求 Running。应写：Connected→Running、Registered→Running 及 speak 前置。

### B5.（P2）声称独立阅读却「同前版语义」

事件表压缩、Phase 1 配置只剩 behavior。v11 补全事件定义与完整 YAML，禁止「同前版」。

---

## 2. 产品侧阻塞（P1）

### B6. Phase 1 不再是可独立交付的最小闭环

最初每阶段做完就能用：握手→register→report→按键上行→收 TTS、CLI、落盘。v10 §10 几乎全是竞态条款。主路径须重新成为 **交付定义**；正确性项进附录。

### B7. keepalive / 360s 从硬约束和验收消失

须写回：周期 **report**、间隔、`last_activity`、空闲超过心跳间隔不掉线。

### B8. 「读循环不阻塞落盘」消失

读循环持续；指令立即进事件总线；落盘/解码不得堵住读。

### B9. playingMode 热更新从 Phase 2 消失

`POST /report` 可热更 playingMode；身份字段必须重连。写入范围与验收。

---

## 3. 非阻塞

- `early_downlink_buf` 回放只驱动计时，不重复 ACK/事件/录帧/落盘；定义容量与溢出。
- 先进入 Stopped，再写 `connection_failed` 并唤醒 waiter（避免立刻 start → 409）。
- `event_log` 容量/TTL；`after_event_seq` 已淘汰时 gap 响应。

---

## 4. 验证缺口

无模拟器实现可跑。实现前仍须真实 core PCM/TTS 冒烟。基线 SHA 无漂移。
