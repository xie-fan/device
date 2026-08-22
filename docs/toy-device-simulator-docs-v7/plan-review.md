# 玩具设备模拟器规划审查（v7）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v7/`  
对照依据：v7 正文；基线 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`（`websocket/controller/handle.go` L1233 起每条管理消息独立 goroutine）  
审查对象：v7 实施计划。本文不改 v7 设计正文。修订见 `docs/toy-device-simulator-docs-v8/`。

**决策：NEEDS_CHANGES**

---

## 1. 结论摘要

v7 已可独立阅读，无跨版本套娃。VAD 先写不改、fault 矩阵、ACK 只看报文标志、SleepMs=0 与 A/B/C 前置均与基线一致。本轮 **4 条 P1**：指令 ACK 阶段边界、report 在途序号、API 生命周期契约、`device_id` 可变导致 Manager 重键。

---

## 2. 阻塞问题

### B1. Phase 1/2 指令 ACK 交付边界互相矛盾

**位置：** `architecture.md` L171；`phase1.md` L121；`phase2.md` L21。

架构写两个阶段运行时规则相同，指令 `need_ack==1` 必须 ACK；Phase 1 只实现/验收音频 ACK；Phase 2 又把指令 ACK 当新增交付。

**应改为：** 运行时规则始终相同。Phase 1 完成音频与指令的 **binary ACK**（SleepMs=0）。Phase 2 只新增 JSON ACK、非零 SleepMs 与节流测试。

### B2. report 回显缺少在途序号模型

**位置：** `architecture.md` L109；`phase1.md` L91。

「刚发出的序号」易被实现成单一 latest。Phase 2 允许 keepalive 与手动 report 共用计数器。服务端对每条管理消息 `go func()`（`handle.go` L1233），`Report` 回显可能乱序。

**应改为：** `pending_reports[sequence]`，按精确序号关联 waiter / `correlation_id`。仅 **初始 report** 的匹配回显可使 Connection 进入 Ready。增加乱序回显验收。禁止用 latest 覆盖未完成的在途项。

### B3. Phase 2 API 缺少统一生命周期契约

**位置：** `phase2.md` L73。

`speak_and_wait` 同时写「wait」与「受理时即返回」，未定义完成条件、超时、调用方断开。`stop` / `delete` 遇到活动 Turn 时是否发 Stage=3、如何 Terminal、释放槽、唤醒 waiter 未定义。

**应改为：** 补 API 状态转移表，写明 `speak`、`speak_and_wait`、`wait`、`stop`、`delete` 的前置状态、原子动作和响应时机。

### B4. `device_id` 修改会破坏 Manager 身份一致性

**位置：** `phase2.md` L98。

fault 或 PUT config 可覆盖 `device_id`，但路径、事件过滤、Turn 槽、录制目录都以它为实例键，未定义原子重键与冲突。

**应改为：** 创建实例后 `device_id` 不可变。`skip_register` 用夹具 ID **新建**实例。

---

## 3. 非阻塞

- `report_echo` 等连接级事件即使当时有活动 Turn 也保持 `correlation_id`。应删掉「此时尚无 Turn」作为原因；keepalive 可发生在 Turn 期间。
- v7 独立可读性、矩阵与 ACK 运行时边界已对齐基线。

---

## 4. 验证缺口

- `expected_server_drop` 首包等待应从进入 `WaitingReply` 起计；首帧匹配后改为下行 idle 计时。否则 Phase 1 `--wait` 负向用例可能悬挂。
- 本轮为静态规划审核，未运行 PCM/TTS 冒烟或 golden。
- 基线 SHA 无工作区漂移；审核时六份 v7 设计文档未改。
