# 玩具设备模拟器规划审查（v5）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v5/`  
对照依据：v5 正文；基线 `5a02d70cdf964bdafea7be92495ad1d0a63499c5` 的 `UpdateMemoryState`（`device_ack.go` L153：SleepMs 与 MemoryPercent 均为 0 则跳过写入）；协议 §9.2 / §10  
审查对象：v5 实施计划。本文不改 v5 正文。修订见 `docs/toy-device-simulator-docs-v6/`。

**决策：NEEDS_CHANGES**

---

## 1. 结论摘要

v4 的 4 个阻塞项已实质进入正文（Reserved CAS、新鲜 ID 夹具、线上 PCM、ACK/SleepMs 主体）。本轮仍有 3 条 P1。

---

## 2. 阻塞问题

### B1. VAD 收口与取消表冲突

**位置：** `architecture.md` L40 与 L106

L40：`vad` 一旦赋值不得改写。取消表在 Speaking 一律写成 `interrupt`；Stage=2 发出后又写成 `stage2`。若已因 Stage=4 记下 `vad`，会被覆盖。

必须定义：收到 Stage=4 时是否立即记录 `vad`。建议：**取消时始终保留已有的非空 `uplink_end_reason`**。Reserved 行「不赋值或 interrupt」必须二选一。

### B2. `sleep_ms: 0` 不能清旧节流，ACK 测试会假通过

**位置：** `phase2.md` L62「0 表示不节流」

基线 `UpdateMemoryState`：MemoryPercent 与 SleepMs 皆 ≤0 则 **直接 return，不写缓存**。旧的正值 SleepMs 保留到 TTL。同一设备先测 binary SleepMs=500 再测 JSON/`sleep_ms:0`，后者即使 ACK 坏了也可能借旧状态通过。

应改为：**0 不产生新的节流状态，不能清除旧状态**。0 / binary / JSON 各用新设备，或明确清理/等待状态过期。

### B3. 指令 ACK 映射与验收不完整

**位置：** `phase2.md` L54 纳入 `need_ack=1` 指令，L72 表只定义音频头来源。

须补 command 的 binary/JSON：Ack / `sequence_number` 取指令序号；DownlinkType=3 / `command`；JSON `topic` 取原指令 topic；`uuid` 明确省略或 0。两种 mode **分别验收**。

---

## 3. 非阻塞

- `bad_seq` 应区分「请求前本地槽为空」与「发帧时服务端无 active ASR」；注入发帧时本地已 Reserved。
- 初始 report、keepalive、手动 `/report` 共用一个原子递增 `sequence_number`。
- README 写 Preparing，正文是 Reserved；`cmd/fixture` 列入 Phase 1 目录。

---

## 4. 验证缺口

- 基线 SHA 无漂移；PCM 受服务端支持。
- 未跑模拟器测试；无 TTS 冒烟、无 golden（已列为实现前置）。
