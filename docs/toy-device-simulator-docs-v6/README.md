# 玩具设备模拟器设计文档（v6）

吸收 `docs/toy-device-simulator-docs-v5/plan-review.md`：`uplink_end_reason` 先写不改、`sleep_ms:0` 不清缓存、指令 ACK 完整映射、bad_seq 本地/服务端前置拆开、report 序号原子递增、`cmd/fixture` 进 Phase 1。

v6 吸收了 v5 审查。**本轮仍为 NEEDS_CHANGES**（ACK 运行时不得查服务端 DownlinkAck），见 `plan-review.md`。修订正文在 `docs/toy-device-simulator-docs-v7/`。

## 文件说明

| 文件 | 内容 |
|------|------|
| `plan-review.md` | v6 规划审查 |
| `architecture.md` | Reserved CAS、先写不改的收口、取消表、fault 矩阵 |
| `phase1.md` | 状态机、report 计数器、`cmd/fixture`、pcm |
| `phase2.md` | 取消表引用、ACK 隔离设备、音频与指令 ACK |
| `phase3.md` | Web UI |
| `phase4.md` | queue、非 pcm、可选清 ACK 缓存 |

## 相对 v5

- Stage=4 **立即**写入 `vad`（若为空）；之后 Stage=2 **不**覆盖
- 取消：**非空 `uplink_end_reason` 一律保留**；Reserved 选定为 **不赋值**
- `sleep_ms: 0` = 不写入节流状态、不能清旧值；0/binary/JSON 分设备
- 指令 ACK：Ack=指令序号，type=3/`command`，JSON topic=原指令 topic，uuid=0/省略；两 mode 分验
- `bad_seq`：本地已 Reserved；服务端无 ASR session
- 所有 report 共用原子 `sequence_number`
## 推荐阅读顺序

1. `plan-review.md`
2. 实现改用 `docs/toy-device-simulator-docs-v7/`
