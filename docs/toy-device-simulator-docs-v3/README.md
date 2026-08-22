# 玩具设备模拟器设计文档（v3）

本目录在 v2 基础上吸收 `docs/toy-device-simulator-docs-v2/plan-review.md` 的全部条款：注入通道与正常路径拆开，对齐基线填实，并写入 v2 非阻塞项。

v3 相对 v2 已关基线 commit、出站注入、`tts_done`、wait 作用域、Stage=4 与 ACK 配置。**规划层 P1 仍未全部关闭**，见 `plan-review.md`。吸收本轮审查的修订正文在 `docs/toy-device-simulator-docs-v4/`。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v3 规划审查（NEEDS_CHANGES：注入矩阵、report 回显、cancel_previous、silence PCM） |
| `architecture.md` | 整体架构、双路径事件模型、硬约束、对齐基线、阶段总览 |
| `phase1.md` | Phase 1：单设备协议闭环、注入通道、one-shot CLI |
| `phase2.md` | Phase 2：批量 + API + Scenario + 精确 silence |
| `phase3.md` | Phase 3：Web UI（只消费 Phase 2 API） |
| `phase4.md` | Phase 4：按需增强 |

## 相对 v2 的主要变更

- 正常路径 vs 注入路径：注入可从 Connected 写帧，绕过本地守卫，outbound 必须进 `frames.jsonl`
- 对齐基线钉死为 `projects/other/ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`（含 MH Seq 例外；非 MH 的 bad_seq 期望为丢弃）
- `expected_server_drop` 为主类型，`inferred_no_reply` 为别名
- 定义 `tts_done`；`POST /wait` 必填 `device_id`
- `expect_downlink_need_ack` 只描述机型预期；客户端见 `NeedAck=1` 必须 ACK
- Speaking + Stage=4 → FinishingUpload 写入状态图
- `cancel_previous` 必须先发 Stage=3 再换 UUID

## 推荐阅读顺序

1. 先读 `plan-review.md`（v3 剩余阻塞）
2. 实现请改用 `docs/toy-device-simulator-docs-v4/`
3. 若只追溯 v3 原文：`architecture.md` → `phase1.md` → `phase2.md`

## 阶段目标速览

- **Phase 1**：单设备主路径 + 可推断失败 + 注入通道 + one-shot CLI
- **Phase 2**：多设备批量 + REST/WS + Scenario + 连续模式
- **Phase 3**：Web UI 调试台（完全复用 Phase 2 API）
- **Phase 4**：指标、回放、压测等按需增强
