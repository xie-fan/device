# 玩具设备模拟器规划审查（v6）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v6/`  
对照依据：v6 正文；基线 `NeedAckEnabled` / `MarkAudioHeaderNeedAck`（`device_ack.go` L55–73：服务端查 `DownlinkAck` 后写入报文 `NeedAck`/`need_ack`）  
审查对象：v6 实施计划。本文不改 v6 正文。修订见 `docs/toy-device-simulator-docs-v7/`。

**决策：NEEDS_CHANGES**

---

## 1. 结论摘要

v5 的三个阻塞项已修正（VAD 先写、SleepMs=0 不清缓存、音频与指令 ACK 映射）。本轮 **1 条 P1**：运行时 ACK 不得依赖模拟器去读服务端 `DownlinkAck` 配置。

---

## 2. 阻塞问题

### B1. ACK 运行时条件错误依赖服务端配置

**位置：** `phase2.md` L35；与 `phase1.md` L51 冲突。

Phase 2 要求同时满足 `DownlinkAck=true` **和** 消息 ACK 标志。模拟器没有查询该配置的入口。服务端先查类型配置，再写入 `NeedAck`/`need_ack`。设备只应看收到的标志。

**应改为：** 运行时 `NeedAck==1` 或 `need_ack==1` **必须 ACK**。`DownlinkAck=true` 只作为集成测试的 **服务端 fixture 前置**（保证报文上会出现标志），不是模拟器运行时查询项。

---

## 3. 非阻塞

- 并非所有事件都有 Turn。注册/上报阶段用 `correlation_id`；Reserved 之后用 `turn_id`。按事件类型写死。
- skip_register 应恢复 v5 细则：run UUID、禁止预注册、`fresh_ids.jsonl` 证据。否则验收只证明命令成功，不证明设备确实未注册。

---

## 4. 验证缺口

- ACK A/B/C 应写明：三台均已 register 且 `status=1`、同一 ACK-enabled 类型、TTS **至少两片**，否则节流间隔不可靠。
- 未跑模拟器/golden；基线 SHA `5a02d70cdf964bdafea7be92495ad1d0a63499c5` 无漂移。
