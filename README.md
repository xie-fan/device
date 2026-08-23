# 玩具设备模拟器

当前落地为 **Phase 1 单设备 CLI**，仅此范围。没有 REST 服务、没有 Manager、没有调试 UI。

## 命令

在 `toy-device-simulator/` 下：

```text
go run ./cmd/check
go run ./cmd/fixture
go run ./cmd/speak
```

## 契约入口

实现与阅读以 [`docs/toy-device-simulator/`](docs/toy-device-simulator/) 为准。

`docs/toy-device-simulator-docs-v*` 与 `docs/toy-device-simulator-docs/` 是冻结历史，不可引用。

## 未落地

以下尚未实现，文档中出现也不代表当前 HEAD 可调用：

- `POST /devices`
- Manager YAML（`manager.yaml`）
- 调试 UI
