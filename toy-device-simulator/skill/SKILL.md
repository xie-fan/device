---
name: simctl
description: "Drive toy-device-simulator via simctl: start the local manager, pick existing devices, send library audio, a whole audio set (音频集), several clips concatenated into one utterance (拼接) or a photo-recognition turn (拍照识别), interpret turn results, or inspect events/frames/audio/history. Use for simulated-device testing and silent/no_reply diagnosis, not simulator source-code development."
---

# simctl

任务级 CLI，对着 manager 说话：选已有设备、送素材库里的音频、读判语。不是 MCP；`cmd/speak` 是独立单设备工具，不是这里的入口。

## 定位与前提

本 skill 目录自带程序和数据，不用进模拟器仓库：

- 下文 `simctl` 指本 skill 目录下的 `bin/simctl`（Windows 是 `bin/simctl.exe`），用绝对路径调用，从哪个目录调都一样。
- 它以本 skill 目录为家：`configs/`（配置树等）、`data/`（素材库、设备册、manager 日志）、`recordings/`（录音）都在这里，都是本机自己的。
- 没有 `bin/` 说明还没安装，按 [references/setup.md](references/setup.md) 装一次。

先读 `simctl --help`，动词和参数以它为准。Manager 地址沿用用户指定值，否则使用帮助中的默认值。

## 首次接入

1. 用 `context` 一次查齐：Manager 是否活着、设备（id/状态/当前挂靠/租约）、挂靠三级树（环境名 → 厂商简称 → 类型简称 → 默认产品，空串表示 run 要带 `--product`）、产品、音频标签计数、音频集、图片资产。`alive=false` 且任务需要时用 `up`，再 `context`；仅查看状态的请求不启动进程。自定义地址在后续调用中保持一致。
2. 从 `context` 里取真实 ID；要看具体素材再用 `assets --tag X`（默认精简字段，`--full` 才全量）。不猜 ID；配置树里没有要测的环境、或素材库里没有要用的音频时，按 [references/setup.md](references/setup.md) 在本机准备（配置树和音频都不入库，各机器自备），或引导用户在调试 UI 准备，再重新查询。**新增音频素材时必须写 `tags` 内容标签**（`POST /assets` 的 `tags` 字段，`对话`/`联网`/`唱歌` 等）——没标签的素材 `run --tag` 选不中。
3. 需要送话时，先读 [references/run.md](references/run.md)，确认选择范围、产品与覆盖、配置副作用，再对选定设备执行 `run`。测拍照识别时再读 [references/photo.md](references/photo.md)（备料、开拍照功能、判读）。只读排障直接查历史或轮次，不为获得结果额外送话。
4. 读取每台设备的结果，依据本次测试目标判断；需要证据时读 [references/inspect.md](references/inspect.md)，用返回的 `instance_id` / `turn_id` 下钻。

已有 Manager、设备和素材时的最短路径（占位符必须替换为查询所得 ID）：

```text
simctl context
simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --asset <asset_id>
# 或按内容标签挑素材（--asset / --tag / --audio-set / --compose 四选一）：
simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --tag 对话
# 或几段拼成一条连续音频、一轮送出（一句话问几件事；asset_id 或标签，逗号分隔）：
simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --compose <asset_id>,<asset_id>,故事
# 或整组送一个音频集（上线验收、定期回归；输出每条音频一个元素）：
simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --audio-set <音频集 id 或名称>
# 拍照识别（服务端下发拍照指令 → 设备传图 → 识图回复；完整参数与判读见 references/photo.md）：
simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --product default --tag 识图 --set audio.format=amr --set behavior.downlink_idle_timeout_sec=6 --set features.photo.enabled=true --set features.photo.image=<图片资产 id>
simctl turn <device_id> --instance <instance_id> --turn <turn_id>
```

## 共享环境与完成条件

`up` 编译并后台启动 Manager，幂等且不自动关；探活用 `GET /devices`，没有 `/healthz`。pid 和日志在本 skill 目录的 `data/manager.pid`、`data/manager.log`。配置树是本 skill 目录的 `configs/registry.yaml`，本机自己的。

Manager 已在运行、要加环境/厂商/设备类型时，用这组接口：
- `POST /registry/environments`，body 为 `name`、`url`，url 可带 `{enterprise}` 占位；可选 `http_url`（App 侧 HTTP 接口基址，http(s)://），不传就按 url 推导：`aichatbotws` 换 `aichatbotwx` 并用 https，路径照留，从 `{占位符}` 段起截掉。`PUT /registry/environments/{env}` 整体替换 `url` 与 `http_url`，同样不传就推导。
- `POST /registry/environments/{env}/enterprises` 和 `POST /registry/environments/{env}/enterprises/{short}/device_types`，body 为 `name`、`short_name`；设备类型还可带 `default_product`（start 不指定产品时用它）。

路径里的环境名要 URL 编码。这组接口会整份重写 `--registry` 指向的文件，文件头注释会丢，改完补回。

人和 agent 共用 Manager。只在用户要求停止时用 `down`，不把它作为测试后的自动清理步骤。

**测试中新建的东西要留下，不要用完就删**，它们是后续测试的素材：
- 新导入的音频、图片，以及 `--compose` 拼出来的组合素材，都留在素材库里。导入时打好内容标签，名字写清楚内容，下次能用 `--tag` 选中或在 `context` 里认出来。
- 为测试加进配置树（本 skill 目录的 `configs/registry.yaml`）的环境、厂商、设备类型留着，文件头注释补回。
- 新建的音频集、产品也留着。
- 用户明确要求时才删。报告里列出这次新增了哪些素材 id 和配置树条目。

业务结果输出 JSON，帮助和参数解析错误还需检查 stderr。`run` 的退出码只表示调用链是否成功，不代表测试通过：退出 `0` 仍可能得到 `no_reply` 或 `error` 判语。逐项检查数组中的 `error` 和 `verdict`，不要只看进程退出码。

测完要把设备复原（停机并清掉临时覆盖）时用 `simctl stop <device_id> --reset`，它先拿租约，别的 run 正用着就报错不停。挂靠三级不会复原，它在下次 start 时由 `run` 重新指定。

完成后报告实际执行范围、结果及关键 ID；区分调用失败、协议结果和业务是否符合预期。无法继续时说明缺失前提或原始错误；未连真实服务端时明确只验证了本地模拟链路。

## 账号与绑定

这几个动词扮演手机 App，打设备所挂环境的 `http_url`（配置树环境上的字段）。`http_url` 必须和该环境 `url` 指向同一个集群：把 `ws://aichatbotws…` 换成 `https://aichatbotwx…`，路径照留。例如测试环境 `ws://aichatbotws.eye4.cn/veepai-test` 对应 `https://aichatbotwx.eye4.cn/veepai-test/`，根路径 `https://aichatbotwx.eye4.cn/` 是生产集群，用户库不通。

1. 先看本 skill 目录的 `data/app_tokens.json` 里有没有该环境的 token。有就直接跳到第 4 步。
2. 有账号就登录：`simctl login --env 测试 --account <邮箱或手机号> --password <密码>`（密码也可放 `SIMCTL_APP_PASSWORD`）。token 按环境存进 `data/app_tokens.json`，输出不回显 token。
3. 没账号先注册，两步。第一步 `simctl register --env 测试 --account <邮箱>` 发验证码，输出 `request_id`。测试邮箱用 `<任意字符串>@test1.mail.anyonstack.com`，收件箱在 Cloud Mail 上。环境变量配了 `SIMCTL_MAIL_URL`（站点根）和 `SIMCTL_MAIL_TOKEN`（或管理员 `SIMCTL_MAIL_ADMIN` + `SIMCTL_MAIL_PASSWORD`）时，第一步带上 `--password` 就一步走完：发码后自己走 Cloud Mail 开放 API 取信、校验、注册；90 秒没收到信输出 `step: mail` 和 `mail_error`，`request_id` 照给，可退回手动第二步。没配就向用户要验证码。第二步原命令加 `--code <验证码> --request-id <request_id> --password <密码>`，校验 + 注册，同样存 token。验证码 10 分钟有效，重发间隔 60 秒。密码 8-16 位、至少两类字符，超长也报 `4000 weak password`。手机号加 `--country CN`。
4. 绑定：`simctl bind <device_id>`，设备没在跑时加 `--env / --enterprise / --device-type [--product]` 拉起来。token 依次取 `--token`、`SIMCTL_APP_TOKEN`、该环境存下的。`unbind` 同参数。

服务端收到 Bind 会下发 `bind/client`，模拟器自动回 `bind/server` code=0。判结果看三项：
- `code`：0 成功；2004 设备 10s 没应答；14013 设备不存在；14014 用户与设备厂商不一致；3101 token 无效。
- `bind_received`：设备端收到下发没有。
- `in_list`：`user/device/Lists` 里有没有它。

2004 按 `connection_state` 分两种。设备不在线（`disconnected`）时 2004 是预期结果。设备 `ready` 却 2004、`bind_received:false`，说明下发没到设备：先查 `http_url` 和 `url` 是不是同一集群，再看设备事件里有没有 `manage_unknown`（收到不认识的管理帧时记下 topic）。

BLE/WiFi 配网走手机本地插件，不在范围内。账号密码和 token 不要写进报告或仓库文件。
