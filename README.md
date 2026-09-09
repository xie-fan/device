# 玩具设备模拟器

语音玩具设备的本地模拟与联调工具：单设备 CLI、Manager / REST / WebSocket、浏览器调试台，以及供 agent 使用的 `simctl`。

已包含 speak backlog、多格式音频与音频库、设备定义持久化、跨重启历史和 Agent CLI（Phase 1–9；完整状态与契约见下方正式文档）。

## 启动调试台

在 `toy-device-simulator/` 下执行（需要 Go；压缩音频转码需要 FFmpeg）：

```text
go run ./cmd/manager --config configs/manager.yaml
```

Manager 默认监听 `127.0.0.1:8090`，浏览器访问 `http://127.0.0.1:8090/`。API 没有认证，默认仅供本机使用。

没有真实服务端时，可在同一目录另开终端执行 `go run ./cmd/echosrv`，作为本地协议联调桩；它不代表真实 ASR/TTS 业务行为。

设备身份使用配置树：环境 → 厂商 → 设备类型 → 设备。默认树在 `configs/registry.yaml`；真实环境地址使用被忽略的 `configs/registry.local.yaml`，启动时通过 `--registry` 指定。

## Agent 接入入口

操作已有设备、送话或排查轮次时，先读 [simctl skill](.claude/skills/simctl/SKILL.md)，再在 `toy-device-simulator/` 执行：

```text
go run ./cmd/simctl --help
```

skill 给出首次接入流程、共享环境操作边界与结果判断规则；参数以 CLI 帮助为准。`cmd/speak` 是独立单设备 CLI，不是 Manager 的 agent 操作入口。

当前 skill 位于 `.claude/skills/simctl`，仅用于 Claude 测试，尚未全局安装；后续计划安装到 `~/.agent` 下。安装位置与模拟器项目位置独立，运行命令前仍需定位项目目录。

## 契约入口

实现与阅读以 [`docs/toy-device-simulator/`](docs/toy-device-simulator/) 为准；领域术语见 [`CONTEXT.md`](CONTEXT.md)。

`docs/toy-device-simulator-docs-v*` 与 `docs/toy-device-simulator-docs/` 是冻结历史，不作为当前实现依据。
