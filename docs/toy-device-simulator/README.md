# 玩具设备模拟器设计文档（正式版）

**状态：审查通过。实现以本目录为准。**

本目录可单独阅读。同目录 `architecture.md` 与 `phase*.md` 可以互相参照；凡被参照的表，本目录内必须有全文。协议正文见 `docs/toy-device-websocket-protocol.md`。

产品：忠实模拟 chatbot、批量并发、Agent API、调试 UI。  
当前落地：Phase 1 CLI（speak / check / fixture）、Phase 2 Manager / REST / WS / Scenario、Phase 3 调试 UI（`GET /`）。  
未落地：Phase 4 speak backlog。  
阶段：Phase 1 单设备 CLI；Phase 2 批量 + REST/WS + Scenario；Phase 3 UI；Phase 4 按需（speak backlog ≠ outbound buffer）。

在 `toy-device-simulator/` 下启动 Manager：`go run ./cmd/manager --config configs/manager.yaml`（默认 `127.0.0.1:8090`）。`POST /devices` 可用。

实现前须对声明的基线提交做 PCM/TTS 冒烟（不要混入该仓库未提交改动），并从 `AudioHeader` 生成 golden。基线见 `architecture.md` §10。

## 实现契约

- live WS：`close_mode=open|drain|abort`。catchup 空且 `open` 才 live。`WriteMessage` 用 `SetWriteDeadline(now+T)`。空闲一次性 timer：T/2 `WriteControl(PingMessage, nil, idle_deadline)`，到 T 看 `last_pong`。禁止用 `SetWriteDeadline` 约束 Ping，禁止零值 deadline。禁止事件 WS `SetReadDeadline`。Pong 经 reader 的 `SetPongHandler`。删除 drain；客户端断开 abort。`T>0`。
- 关停入口锁所有权唯一：`requestCloseLocked` 必须已持 `device_mu` 且禁止 IO；`requestClose` 必须未持锁。`abort` / `finishAbort` 必须未持锁。timeout 半开失败锁内 `requestCloseLocked(abort)`，显式解锁后再 Close。禁止二次加锁、禁止持锁 Ping/Close。
- outbound 单一物理队列：数据 `len >= depth-1` 拒绝，Stage=3 `len >= depth` 拒绝。`write_queue_depth >= 2`。`CancelTurn` / `BeginClose` 返回 `CancelResult` / `CloseResult`；`backpressure` 经 `appendEventLocked` 进入对应模板的 `acc`。BeginClose 的 `len` 取过滤后的 keep。

## 文件说明

| 文件 | 内容 |
|------|------|
| `architecture.md` | 完整契约：Turn、WS hub、关停锁所有权、Ping deadline、队列准入、terminalLocked |
| `phase1.md` | CLI；完整矩阵与 YAML |
| `phase2.md` | 完整 API |
| `phase3.md` | UI |
| `phase4.md` | speak backlog / 非 pcm / 探针 |

## 阅读顺序

1. `architecture.md`
2. `phase1.md` → `phase2.md` → `phase3.md`
3. `phase4.md` 按需
