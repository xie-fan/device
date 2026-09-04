---
name: simctl
description: "Drive toy-device-simulator via simctl: start the local manager, pick a device, send library audio, read a turn verdict, or inspect events/frames/audio/history. Use when talking to simulated devices, debugging a silent/no_reply turn, or exploring manager state as an agent."
---

# simctl

任务级 CLI，对着本仓的 manager 说话：选已有设备、送一段音频库素材、读判语。不是 MCP，也不建/删设备。

在 `toy-device-simulator/` 下跑。动词和参数以 `go run ./cmd/simctl --help` 为准（skill 不抄，避免和代码漂）。

## 起 manager

`simctl up`：先编译再后台拉起，幂等，**不自动关**。探活是 `GET /devices`，没有 `/healthz`。pid / 日志在 `data/manager.pid`、`data/manager.log`。只有显式 `down` 才停。

人和 agent 共用这一个进程。

## 两份参考

- [references/run.md](references/run.md) — 选设备 → 送话 → 读判语（含终止表、六值、`overridden`）
- [references/inspect.md](references/inspect.md) — 事件、帧、音频、历史运行
