# 玩具设备模拟器设计文档（审查后修订版）

本目录包含 v2 设计文档。已吸收 v1 审查的多数条款，**仍有 2 条阻塞项**，见 `plan-review.md`。未写注入通道、未填对齐基线前，不得开始 Phase 1 实现。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v2 规划审查（决策、剩余阻塞项、建议改入各文件的条文） |
| `architecture.md` | 整体架构、原则、事件模型、硬约束、技术选型、阶段总览 |
| `phase1.md` | Phase 1 详细设计（单设备协议正确闭环） |
| `phase2.md` | Phase 2 详细设计（批量 + API + Scenario） |
| `phase3.md` | Phase 3 详细设计（Web UI） |
| `phase4.md` | Phase 4 按需增强说明 |

## 推荐阅读顺序

1. 先读 `plan-review.md`，确认剩余阻塞项与拟改条文
2. 再读 `architecture.md` 了解整体（注入路径、对齐基线以审查结论为准）
3. 按阶段阅读 `phase1.md` → `phase2.md` → `phase3.md`
4. `phase4.md` 按需参考

## 审查后主要变更摘要

- Phase 1 Core **推荐 Go**（解决 TextMessage 承载二进制音频的收包问题）
- 事件模型改为：`local_validation_error` / `expected_server_drop` / `server_observed_drop`
- Phase 1 CLI 强制 **one-shot**（`speak --config --audio [--wait]`）
- Golden 权威源改为协议文档 + `types.AudioHeader`（禁止 mock.go）
- 示例 device_type 去掉 MH 前缀
- 状态机拆成正交三套：连接 / 上行 Turn / 下行播放
- Phase 2 增加 Turn 并发规则（默认 409）并补齐 UI 所需查询/配置 API
- silence 精确定义为按真实节奏发送静音/零采样帧
- 增加对齐基线（仓库 + commit）与最小联调前置

## 阶段目标速览

- **Phase 1**：单设备完整跑通协议主路径，失败可推断，one-shot CLI 可用
- **Phase 2**：多设备批量 + REST/WS API（含并发规则与查询接口）+ Scenario + 连续模式
- **Phase 3**：Web UI 调试台（完全复用 Phase 2 API）
- **Phase 4**：指标、回放、压测等按需增强
