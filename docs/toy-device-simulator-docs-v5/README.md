# 玩具设备模拟器设计文档（v5）

吸收 `docs/toy-device-simulator-docs-v4/plan-review.md`：原子 Reserved Turn、skip_register 由夹具证明新 ID、线上 pcm + 先编码后分帧、Phase 2 ACK/SleepMs 契约。

v5 吸收了 v4 审查。**本轮仍为 NEEDS_CHANGES**（VAD 与取消表、`sleep_ms:0` 不清缓存、指令 ACK），见 `plan-review.md`。修订正文在 `docs/toy-device-simulator-docs-v6/`。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v5 规划审查 |
| `architecture.md` | Reserved Turn、fault 矩阵、pcm 管线、对齐基线 |
| `phase1.md` | Reserved 状态、report 按 sequence_number、夹具 ID、线上 pcm |
| `phase2.md` | 取消转移表、ACK 契约、silence 编码顺序 |
| `phase3.md` | Web UI |
| `phase4.md` | queue、非 pcm 线上格式、Mongo 预检可选 |

## 相对 v4

- speak 受理时原子 `Reserved`；active 从 Reserved 到 terminal
- 全状态 cancel 转移表（含 Reserved、FinishingUpload）
- skip_register 不要求 CLI 查库；由测试夹具分配并证明全新 ID
- Phase 1/2 线上 `format` **强制 pcm**；WAV 只作源文件；PCM 时间轴 →（恒等）→ 对 PCM 字节分帧
- Phase 2 定义 ACK mode / 字段映射 / SleepMs 与节流验收
- 默认 reject，可选 cancel_previous，不交付 queue
## 推荐阅读顺序

1. `plan-review.md`
2. 实现改用 `docs/toy-device-simulator-docs-v6/`
