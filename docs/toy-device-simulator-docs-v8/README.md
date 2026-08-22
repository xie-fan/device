# 玩具设备模拟器设计文档（v8）

本目录设计正文冻结。审查见 `plan-review.md`（NEEDS_CHANGES）。实施改用 `docs/toy-device-simulator-docs-v9/`。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v8 规划审查 |
| `architecture.md` | Turn、事件、在途 report、下行计时、注入、ACK 阶段边界、身份不可变、对齐基线 |
| `phase1.md` | 单设备闭环；binary ACK（音频+指令）；pending_reports；`--wait` 计时 |
| `phase2.md` | Manager API 生命周期表；JSON ACK 与节流；`device_id` 不可变 |
| `phase3.md` | Web UI |
| `phase4.md` | 按需增强 |

## 推荐阅读顺序

1. `plan-review.md`
2. 实施改用 `docs/toy-device-simulator-docs-v9/`

## 阶段目标

- **Phase 1**：握手→注册→report（在途序号）→pcm 上行→收 TTS；音频与指令 **binary ACK**；注入按矩阵；one-shot CLI + 夹具
- **Phase 2**：多设备、REST/WS、Scenario；JSON ACK、非零 SleepMs、节流 A/B/C；统一 API 生命周期
- **Phase 3**：Web UI，只消费 Phase 2 API
- **Phase 4**：queue、非 pcm 上线、可选探针等
