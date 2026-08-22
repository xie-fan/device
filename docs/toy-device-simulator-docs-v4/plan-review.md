# 玩具设备模拟器规划审查（v4）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v4/`  
对照依据：v4 正文；基线 `5a02d70cdf964bdafea7be92495ad1d0a63499c5`（`LookupCachedDevicePlayMode`、`ThrottleBeforeDownlink`）；协议 §10 ACK  
审查对象：v4 实施计划。本文不改 v4 正文。修订见 `docs/toy-device-simulator-docs-v5/`。

**决策：NEEDS_CHANGES**

---

## 1. 结论摘要

v3 的 4 个原始 P1 已实质落入 v4（fault 矩阵、report 回显、Stage=2 历史事实、内部 mono s16le）。本轮仍有 4 条新 P1，不能开工。

| 类别 | 处理 |
|------|------|
| 阻塞 4 | 原子 Turn 占用、skip_register 前置证明、编码/分片顺序、Phase 2 ACK 契约 |
| 非阻塞 2 | report 用 sequence_number 关联；并发措辞 |
| 验证缺口 | 基线 SHA 仍匹配；无 TTS 冒烟、无 golden |

---

## 2. 阻塞问题

### B1. Turn 占用不是原子操作

**位置：** `architecture.md` §4.1 L50；`phase2.md` §4 L27

active 起点写成「第一帧发出」时，两个并发 `/speak` 可在发帧前同时通过检查。`cancel_previous` 未覆盖 **FinishingUpload**。

必须在**请求受理时**原子创建 `Preparing`/`Reserved` Turn，并给出全部状态的取消转移表。

### B2. skip_register 前置条件 CLI 查不到

**位置：** `phase1.md` §8 L132

要求 CLI 在已有 `status=1` 时拒绝，但配置和 API 都没有 Mongo/core 查询通道。服务端先查内存缓存再查库（`support.go` L69）。

应采用：数据库预检、core 查询接口，或**测试夹具生成并证明全新 ID**。不能让 CLI 做它查不到的判断。

### B3. WAV 编码与分片顺序不唯一

**位置：** `phase2.md` §7 L79–82；`phase1.md` 默认 `format: wav`

先得到 PCM，再「切片后编码为线上 format」。`format=wav` 时逐片编码会多个 RIFF 头，与「无重复 RIFF」冲突。

钉死：**PCM 时间轴 → 单个流式/整段编码器 → 对编码流分帧**；或 Phase 1/2 **线上 format 强制 pcm**。

### B4. Phase 2 JSON ACK / SleepMs 无实现契约

**位置：** `phase2.md` §2 L11 列为交付，后文无配置、触发、字段、验收。

至少定义：binary/JSON 选择、uuid/sequence_number/downlink_type 映射、可配置 SleepMs、服务端节流生效的验收。

---

## 3. 非阻塞

- report 回显应按 `sequence_number` 关联，避免误收周期 keepalive 的延迟回显。
- 并发措辞统一为：**默认 reject，可选 cancel_previous，不交付 queue**。

---

## 4. 验证缺口

- 基线 commit `5a02d70cdf964bdafea7be92495ad1d0a63499c5` 无漂移。
- 未跑真实 TTS 冒烟，未生成 golden（文档已列为实现前置）。
