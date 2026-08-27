# 玩具设备模拟器

当前落地为 **Phase 1 单设备 CLI**、**Phase 2 Manager / REST / WS / Scenario** 与 **Phase 3 调试 UI**。  
Phase 4 speak backlog 尚未落地。

## 命令

在 `toy-device-simulator/` 下：

```text
go run ./cmd/check
go run ./cmd/fixture
go run ./cmd/speak
go run ./cmd/manager --config configs/manager.yaml
```

Manager 默认监听 `127.0.0.1:8090`。浏览器打开 `http://127.0.0.1:8090/` 即调试台。`POST /devices` 可用。模板目录若要用仓库内文件，加 `--templates configs/templates`。

## 契约入口

实现与阅读以 [`docs/toy-device-simulator/`](docs/toy-device-simulator/) 为准。

`docs/toy-device-simulator-docs-v*` 与 `docs/toy-device-simulator-docs/` 是冻结历史，不可引用。

## 未落地

以下尚未实现，文档中出现也不代表当前 HEAD 可调用：

- speak backlog（Phase 4）
