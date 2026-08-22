# 玩具设备模拟器设计文档（v4）

本目录吸收 `docs/toy-device-simulator-docs-v3/plan-review.md` 的 4 条 P1：逐 fault 矩阵、report 回显不是 ACK、`cancel_previous` 不篡改已完成上行、内部 PCM 时间轴。

v4 吸收了 v3 审查的 4 条 P1。**本轮仍为 NEEDS_CHANGES**，见 `plan-review.md`。修订正文在 `docs/toy-device-simulator-docs-v5/`。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v4 规划审查（原子 Turn、skip_register 夹具、编码顺序、ACK 契约） |
| `architecture.md` | 架构、事件、fault 矩阵、对齐基线 |
| `phase1.md` | Phase 1：状态机、完整 fault 矩阵、内部 PCM、one-shot CLI |
| `phase2.md` | Phase 2：active turn 直到 terminal、cancel_previous 原因字段、silence 解封装 |
| `phase3.md` | Phase 3：Web UI |
| `phase4.md` | Phase 4：按需增强 |

## 相对 v3 的主要变更

- 注入验收改为逐 fault 矩阵；禁止「凡注入必 drop」
- `skip_register` 必须用确认不在库的新 `device_id`
- `skip_report` 不默认 `expected_server_drop`
- `bad_header` 固定为剥 `'0'` 后 <100 字节
- `dup_uuid` / 任意错误 Stage 不作为 drop 验收
- `ack_failure` 仅 register；Ready 等 `/report/client` 的 `ReportData` 回显
- active turn 持续到 Turn terminal；已 Stage=2 时 `uplink_end_reason` 保持 `stage2`
- 内部 PCM：mono signed 16-bit LE；WAV 解封装后再进时间轴
- Phase 2 先只交付并发策略 `reject`；`cancel_previous` 按正确原因字段交付；`queue` 推迟

## 推荐阅读顺序

1. `plan-review.md`
2. 实现改用 `docs/toy-device-simulator-docs-v5/`
