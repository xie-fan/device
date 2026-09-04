# 玩具设备模拟器设计文档（正式版）

**状态：审查通过。实现以本目录为准。**

本目录可单独阅读。同目录 `architecture.md` 与 `phase*.md` 可以互相参照；凡被参照的表，本目录内必须有全文。协议正文见 `docs/toy-device-websocket-protocol.md`。

产品：忠实模拟 chatbot、批量并发、Agent API、调试 UI。  
当前落地：Phase 1 CLI（speak / check / fixture）、Phase 2 Manager / REST / WS / Scenario、Phase 3 调试 UI（`GET /`）、Phase 4 全部主项——speak backlog、静默成功探针、raw PCM 上传、全局事件总线（`/ws/events/global`）、断开即 interrupt、wav 推流、Phase 5 多格式音频——设备可配 mp3/amr/aac 线上格式（含码率）、持久化音频库（导入/筛选/编辑/试听）、格式不符自动 ffmpeg 转码、压缩格式 `-re` 限速推流、下行/回放格式感知（契约见 phase5.md）、Phase 6 多格式本地闭环——echosrv 按上行格式回 TTS 且分包形态照抄真实服务端、音频库解码试听、mp3 去 ID3（见 phase6.md）、Phase 7 设备定义持久化——设备落盘 `data/devices.yaml` 跨重启存活，「定义」与「本次运行的当前值」分两层，独立的设备管理视图（见 phase7.md）、Phase 8 跨重启的历史——旧 `instance_id` 的 turn / 帧 / 音频 / 事件不再 404（盘就是真相源，事件落盘 `events.jsonl`），`GET /devices/{id}/instances` 列出每一次运行，界面「历史运行」抽屉可回看（见 phase8.md）、Phase 9 给 agent 的工具——`cmd/simctl`（skills + CLI，不是 MCP），九个动词，`run` 合并 speak_and_wait 与 GET /turns 给出判语（见 phase9.md）。  
阶段：Phase 1 单设备 CLI；Phase 2 批量 + REST/WS + Scenario；Phase 3 UI；Phase 4 按需增强（speak backlog ≠ outbound buffer；见 phase4.md）；Phase 5 多格式音频 + ffmpeg 管线（见 phase5.md）；Phase 6 多格式的本地可重复验收（见 phase6.md）；Phase 7 设备定义持久化与设备管理（见 phase7.md）；Phase 8 跨重启的历史（见 phase8.md）；Phase 9 给 agent 的工具（见 phase9.md）。

设备身份走配置树（环境 → 厂商 → 设备类型 → 设备，`/registry`，落盘 `configs/registry.yaml`）：厂商/类型有名称与简称，wire 值与环境 url 占位符都用简称；设备创建引用树路径，不再平铺身份字段（phase2.md §6.10）。Phase 1 CLI 仍用单机平铺 YAML。

在 `toy-device-simulator/` 下启动 Manager：`go run ./cmd/manager --config configs/manager.yaml`（默认 `127.0.0.1:8090`；`--registry` 可改树落盘路径）。`POST /devices` 可用。启动日志打印 ffmpeg 编码能力（`manager.yaml` 的 `ffmpeg_path` 留空查 PATH；AMR 编码需 full 构建）；无 ffmpeg 时 pcm/wav 全链路照常，压缩格式路径 400。

本地无真实服务端时，可另起 `go run ./cmd/echosrv`（默认 `127.0.0.1:8089`，与 `configs/registry.yaml` 默认环境一致）：注册即 ack、report 回显、收上行后慢推一段 440Hz TTS，设备靠 `downlink_idle_timeout_sec` 收尾——足够跑通 UI 全链路与 backlog 排队验收。调试 UI 已支持 Phase 4：配置表单含 `speak_backlog_depth`／`silence_probe`／`interrupt_on_disconnect` 与 `audio.format=wav`，设备状态行显示排队数，右栏「全局」页签消费 `/ws/events/global`。

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
| `phase4.md` | 按需增强（backlog / 探针 / raw PCM / 全局总线 / 断开打断 / wav 推流） |
| `phase5.md` | 多格式音频（mp3/amr/aac 设备格式 / 音频库 / ffmpeg 转码与 -re 推流 / 格式感知回放） |
| `phase6.md` | 多格式的本地闭环（echosrv 按格式回 TTS 与分包拟真 / 音频库解码试听 / mp3 去 ID3 / 与真实服务端的格式对照表） |
| `phase7.md` | 设备定义持久化（`data/devices.yaml`）/ 定义与当前值两层 / definition 与 reset 端点 / 设备管理视图 |
| `phase8.md` | 跨重启的历史（instance 三种来源 / 事件落盘 `events.jsonl` / `GET /instances` / 上行格式自描述 / 历史运行视图） |
| `phase9.md` | 给 agent 的工具（`cmd/simctl` / 九个动词 / 判语 / `down_bytes`） |

## 阅读顺序

1. `architecture.md`
2. `phase1.md` → `phase2.md` → `phase3.md`
3. `phase4.md`、`phase5.md`、`phase6.md`、`phase7.md`、`phase8.md`、`phase9.md` 按需
