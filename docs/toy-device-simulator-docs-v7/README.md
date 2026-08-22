# 玩具设备模拟器设计文档（v7）

本目录设计正文冻结。审查见 `plan-review.md`（NEEDS_CHANGES）。实施改用 `docs/toy-device-simulator-docs-v8/`。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v7 规划审查 |
| `architecture.md` | 目标、原则、Turn/事件/注入/ACK、硬约束、音频管线、对齐基线 |
| `phase1.md` | 单设备协议闭环、状态机、配置、CLI 与夹具、验收 |
| `phase2.md` | 批量、API、Scenario、silence、音频与指令 ACK、验收 |
| `phase3.md` | Web UI 调试台 |
| `phase4.md` | 按需增强 |

## 推荐阅读顺序

1. `plan-review.md`
2. 实施改用 `docs/toy-device-simulator-docs-v8/`

## 阶段目标

- **Phase 1**：单设备握手→注册→report→pcm 上行→收 TTS；注入按矩阵；one-shot CLI + 夹具
- **Phase 2**：多设备、REST/WS、Scenario、ACK 契约、查询 API
- **Phase 3**：Web UI，只消费 Phase 2 API
- **Phase 4**：queue、非 pcm 上线、可选探针等
