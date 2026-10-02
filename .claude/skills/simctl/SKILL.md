---
name: simctl
description: "Drive toy-device-simulator via simctl: start the local manager, pick existing devices, send library audio, a whole audio set (音频集), several clips concatenated into one utterance (拼接) or a photo-recognition turn (拍照识别), interpret turn results, or inspect events/frames/audio/history. Use for simulated-device testing and silent/no_reply diagnosis, not simulator source-code development."
---

# simctl

任务级 CLI，对着 manager 说话：选已有设备、送素材库里的音频、读判语。不是 MCP；`cmd/speak` 是独立单设备工具，不是这里的入口。

## 定位与前提

先从用户提供的路径或当前工作区定位模拟器仓库，确认 `toy-device-simulator/go.mod` 和 `cmd/simctl` 存在，再进入 `toy-device-simulator/`。全局安装的 skill 目录不是项目目录；没有可确认的仓库路径时向用户询问，不从 skill 安装位置推算。

下文 `simctl` 均指在上述目录执行 `go run ./cmd/simctl`，无需预装同名命令。先读 `go run ./cmd/simctl --help`，动词和参数以它为准。运行需要可用的 Go 工具链；Manager 地址沿用用户指定值，否则使用帮助中的默认值。

## 首次接入

1. 用 `context` 一次查齐：Manager 是否活着、设备（id/状态/当前挂靠/租约）、挂靠三级树（环境名 → 厂商简称 → 类型简称 → 默认产品，空串表示 run 要带 `--product`）、产品、音频标签计数、音频集、图片资产。`alive=false` 且任务需要时用 `up`，再 `context`；仅查看状态的请求不启动进程。自定义地址在后续调用中保持一致。
2. 从 `context` 里取真实 ID；要看具体素材再用 `assets --tag X`（默认精简字段，`--full` 才全量）。不猜 ID；配置树里没有要测的环境、或素材库里没有要用的音频时，按 [references/setup.md](references/setup.md) 在本机准备（配置树和音频都不入库，各机器自备），或引导用户在调试 UI 准备，再重新查询。**新增音频素材时必须写 `tags` 内容标签**（`POST /assets` 的 `tags` 字段，`对话`/`联网`/`唱歌` 等）——没标签的素材 `run --tag` 选不中。
3. 需要送话时，先读 [references/run.md](references/run.md)，确认选择范围、产品与覆盖、配置副作用，再对选定设备执行 `run`。测拍照识别时再读 [references/photo.md](references/photo.md)（备料、开拍照功能、判读）。只读排障直接查历史或轮次，不为获得结果额外送话。
4. 读取每台设备的结果，依据本次测试目标判断；需要证据时读 [references/inspect.md](references/inspect.md)，用返回的 `instance_id` / `turn_id` 下钻。

已有 Manager、设备和素材时的最短路径（占位符必须替换为查询所得 ID）：

```text
go run ./cmd/simctl context
go run ./cmd/simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --asset <asset_id>
# 或按内容标签挑素材（--asset / --tag / --audio-set / --compose 四选一）：
go run ./cmd/simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --tag 对话
# 或几段拼成一条连续音频、一轮送出（一句话问几件事；asset_id 或标签，逗号分隔）：
go run ./cmd/simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --compose <asset_id>,<asset_id>,故事
# 或整组送一个音频集（上线验收、定期回归；输出每条音频一个元素）：
go run ./cmd/simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --audio-set <音频集 id 或名称>
# 拍照识别（服务端下发拍照指令 → 设备传图 → 识图回复；完整参数与判读见 references/photo.md）：
go run ./cmd/simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --product default --tag 识图 --set audio.format=amr --set behavior.downlink_idle_timeout_sec=6 --set features.photo.enabled=true --set features.photo.image=<图片资产 id>
go run ./cmd/simctl turn <device_id> --instance <instance_id> --turn <turn_id>
```

## 共享环境与完成条件

`up` 编译并后台启动 Manager，幂等且不自动关；探活用 `GET /devices`，没有 `/healthz`。pid 和日志在项目的 `data/manager.pid`、`data/manager.log`。真实服务地址使用本地配置树（`up --registry`，见帮助），不写入受跟踪配置。

Manager 已在运行、要加环境/厂商/设备类型时，用这组接口：
- `POST /registry/environments`，body 为 `name`、`url`，url 可带 `{enterprise}` 占位。
- `POST /registry/environments/{env}/enterprises` 和 `POST /registry/environments/{env}/enterprises/{short}/device_types`，body 为 `name`、`short_name`；设备类型还可带 `default_product`（start 不指定产品时用它）。

路径里的环境名要 URL 编码。这组接口会整份重写 `--registry` 指向的文件，文件头注释会丢，改完补回。

人和 agent 共用 Manager。只在用户要求停止时用 `down`，不把它作为测试后的自动清理步骤。

**测试中新建的东西要留下，不要用完就删**，它们是后续测试的素材：
- 新导入的音频、图片，以及 `--compose` 拼出来的组合素材，都留在素材库里。导入时打好内容标签，名字写清楚内容，下次能用 `--tag` 选中或在 `context` 里认出来。
- 为测试加进配置树（`registry.local.yaml`）的环境、厂商、设备类型留着，文件头注释补回。
- 新建的音频集、产品也留着。
- 用户明确要求时才删。报告里列出这次新增了哪些素材 id 和配置树条目。

业务结果输出 JSON，帮助和参数解析错误还需检查 stderr。`run` 的退出码只表示调用链是否成功，不代表测试通过：退出 `0` 仍可能得到 `no_reply` 或 `error` 判语。逐项检查数组中的 `error` 和 `verdict`，不要只看进程退出码。

测完要把设备复原（停机并清掉临时覆盖）时用 `simctl stop <device_id> --reset`，它先拿租约，别的 run 正用着就报错不停。挂靠三级不会复原，它在下次 start 时由 `run` 重新指定。

完成后报告实际执行范围、结果及关键 ID；区分调用失败、协议结果和业务是否符合预期。无法继续时说明缺失前提或原始错误；未连真实服务端时明确只验证了本地模拟链路。
