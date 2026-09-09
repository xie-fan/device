---
name: simctl
description: "Drive toy-device-simulator via simctl: start the local manager, pick existing devices, send library audio, interpret turn results, or inspect events/frames/audio/history. Use for simulated-device testing and silent/no_reply diagnosis, not simulator source-code development."
---

# simctl

任务级 CLI，对着 manager 说话：选已有设备、送音频库素材、读判语。不是 MCP；`cmd/speak` 是独立单设备工具，不是这里的入口。

## 定位与前提

先从用户提供的路径或当前工作区定位模拟器仓库，确认 `toy-device-simulator/go.mod` 和 `cmd/simctl` 存在，再进入 `toy-device-simulator/`。全局安装的 skill 目录不是项目目录；没有可确认的仓库路径时向用户询问，不从 skill 安装位置推算。

下文 `simctl` 均指在上述目录执行 `go run ./cmd/simctl`，无需预装同名命令。先读 `go run ./cmd/simctl --help`，动词和参数以它为准。运行需要可用的 Go 工具链；Manager 地址沿用用户指定值，否则使用帮助中的默认值。

## 首次接入

1. 用 `status` 检查 Manager。已运行就复用；未运行且任务需要启动时用 `up`，再确认状态。仅查看状态的请求不启动进程。自定义地址在后续调用中保持一致。
2. 用 `devices` 和 `assets` 获取真实 ID，确认目标设备所属环境和素材。不猜 ID；列表为空时说明缺少什么，引导用户在调试 UI 准备设备或导入素材，再重新查询。设备与素材准备不在此 CLI 的范围内。
3. 需要送话时，先读 [references/run.md](references/run.md)，确认选择范围和配置副作用，再对选定设备执行 `run`。只读排障直接查历史或轮次，不为获得结果额外送话。
4. 读取每台设备的结果，依据本次测试目标判断；需要证据时读 [references/inspect.md](references/inspect.md)，用返回的 `instance_id` / `turn_id` 下钻。

已有 Manager、设备和素材时的最短路径（占位符必须替换为查询所得 ID）：

```text
go run ./cmd/simctl status
go run ./cmd/simctl devices
go run ./cmd/simctl assets
go run ./cmd/simctl run <device_id> --asset <asset_id>
go run ./cmd/simctl turn <device_id> --instance <instance_id> --turn <turn_id>
```

## 共享环境与完成条件

`up` 编译并后台启动 Manager，幂等且不自动关；探活用 `GET /devices`，没有 `/healthz`。pid 和日志在项目的 `data/manager.pid`、`data/manager.log`。真实服务地址使用本地配置树（`up --registry`，见帮助），不写入受跟踪配置。

人和 agent 共用 Manager。只在用户要求停止时用 `down`，不把它作为测试后的自动清理步骤。

业务结果输出 JSON，帮助和参数解析错误还需检查 stderr。`run` 的退出码只表示调用链是否成功，不代表测试通过：退出 `0` 仍可能得到 `no_reply` 或 `error` 判语。逐项检查数组中的 `error` 和 `verdict`，不要只看进程退出码。

完成后报告实际执行范围、结果及关键 ID；区分调用失败、协议结果和业务是否符合预期。无法继续时说明缺失前提或原始错误；未连真实服务端时明确只验证了本地模拟链路。
