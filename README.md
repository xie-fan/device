# 玩具设备模拟器

语音玩具设备的本地模拟与联调工具：单设备 CLI、Manager / REST / WebSocket、浏览器调试台，以及供 agent 使用的 `simctl`。

已包含 speak backlog、多格式音频与素材库、跨重启历史、Agent CLI、设备租约，以及按产品配置的音频格式、对话模式与拍照（Phase 1–12；完整状态与契约见下方正式文档）。

## 启动调试台

在 `toy-device-simulator/` 下执行（需要 Go；压缩音频转码需要 FFmpeg）：

```text
go run ./cmd/manager --config configs/manager.yaml
```

Manager 默认监听 `127.0.0.1:8090`，浏览器访问 `http://127.0.0.1:8090/`。API 没有认证，默认仅供本机使用。

没有真实服务端时，可在同一目录另开终端执行 `go run ./cmd/echosrv`，作为本地协议联调桩；它不代表真实 ASR/TTS 业务行为。

设备身份使用配置树：环境 → 厂商 → 设备类型 → 设备。默认树在 `configs/registry.yaml`；真实环境地址使用被忽略的 `configs/registry.local.yaml`，启动时通过 `--registry` 指定。

## Agent 接入入口

agent 通过 simctl skill 操作已有设备、送话或排查轮次。skill 源文件在 [`toy-device-simulator/skill/`](toy-device-simulator/skill/SKILL.md)，在 `toy-device-simulator/` 下安装一次：

```text
go run ./cmd/simctl install
```

装到 `~/.agents/skills/simctl/`，`~/.claude/skills/simctl` 是指向它的目录链接：程序在 `bin/`，配置、素材库、录音也都在这个目录里（本机自己的，不入库），之后从任何目录都能用，不用进仓库。仓库更新后重跑一次，程序和文档覆盖成新版本，本机配置和数据不动。安装与本机准备见 [setup.md](toy-device-simulator/skill/references/setup.md)。

skill 给出首次接入流程、共享环境操作边界与结果判断规则；参数以 `simctl --help` 为准。`cmd/speak` 是独立单设备 CLI，不是 Manager 的 agent 操作入口。

## 契约入口

实现与阅读以 [`docs/toy-device-simulator/`](docs/toy-device-simulator/) 为准；领域术语见 [`CONTEXT.md`](CONTEXT.md)。

`docs/toy-device-simulator-docs-v*` 与 `docs/toy-device-simulator-docs/` 是冻结历史，不作为当前实现依据。

## TODO

- [ ] ack：开启后设备按内存大小向服务端上报 ack，服务端下发音频时据此暂停一段时间，防止下发的音频撑爆设备内存
