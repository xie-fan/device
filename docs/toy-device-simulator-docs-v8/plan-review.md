# 玩具设备模拟器规划审查（v8）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v8/`  
对照依据：v8 正文；基线 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
（`turn_stage_command.go` L109 `SyncDevice` 纯指令；L154 `ReplyModeText` 成功 JSON；L180 空 `replyText` 无音频）  
审查对象：v8 实施计划。本文不改 v8 设计正文。修订见 `docs/toy-device-simulator-docs-v9/`。

**决策：NEEDS_CHANGES**

---

## 1. 结论摘要

v7 的四条 P1 已实质修复。本轮 **3 条 P1**：Turn 完成条件绑死 TTS；`POST /wait` 事后注册丢失唤醒；连接异常无统一收口，实例可能卡在 Starting/Running。

---

## 2. 阻塞问题

### B1. Turn 完成条件错误绑定 TTS

**位置：** `architecture.md` L123–146。

只有匹配 UUID 的 TTS 才取消首包计时并结束 Turn。基线另有：`voiceSyncCommands` → `SyncDevice`（`'1'` `/command/client`）、`ReplyModeText` → `SendWebsocketJSON`（无前缀 `Code=0`）、空 `replyText` 直接 `return true`（设备侧可能无终态下行）。这些合法轮次会被等到 20s 并标 timeout。

**应改为：** 给出 TTS / command / asr_result / 成功 JSON / command+TTS 的关联与终止矩阵。`tts_done` 仅表示有 TTS；Turn Terminal 由「相关下行」集合与 settle 计时决定。

### B2. `POST /wait` 丢失唤醒竞态

**位置：** `phase2.md` L73–114。

`speak` CAS 后立即返回；`wait` 事后注册 waiter。若两次请求之间 Turn 已 Terminal，调用方只能 504。

**应改为：** 同一把锁（或版本号）内执行「检查当前状态 + 注册 waiter」。已 Terminal 立即 200。按事件等待须带历史游标 `after_event_seq`，或写明省略游标时仅等待未来事件。

### B3. 异常连接失败没有生命周期收口

**位置：** `phase2.md` L37–47、`start` 行「失败走事件」。

握手失败、读写错误、远端关闭、report 发送失败未定义实例落到哪一态。当前只有主动 `stop` 清理 Turn、计时器、`pending_reports` 与 waiter。

**应改为：** 统一 `fail_connection` 路径，清理后进入 **Stopped**（可再次 `start`），并唤醒 waiter、发出 `connection_failed`。

---

## 3. 非阻塞

- report 计数器：初值 1 再 `atomic_add` 按 Go 常见语义会得到 2。应写明第一个发出的序号等于 `report_sequence_start`。
- 默认等待 30s 可能小于「上传 + 首包 20s + TTS + idle 20s」。应给出超时预算公式，Scenario 不得写死偏小的 30。

---

## 4. 验证缺口

- 静态规划审核；无模拟器实现，未跑功能测试。
- 基线 SHA 无漂移；审核时六份 v8 设计文档未改。
- 本仓库当时不是 Git 工作区，无 git clean/diff。
