# 新环境准备

仓库里只有代码、skill 和默认配置。**配置树里你自己的环境和音频素材都不入库**，在每台机器上各自准备，准备好就一直留着用，不要用完就删。

## 1. 前置

- Go 1.22 及以上。
- 音频转码程序（可选）：设备用压缩格式（amr / mp3 / aac）或导入非 wav 素材时需要。`configs/manager.yaml` 里有一项填它的路径，留空则从 PATH 里找；找不到时只支持 pcm / wav。

## 2. 拉仓库即可用的部分

- skill 在 `.claude/skills/simctl/`，在仓库目录里启动 agent 就能用，不用另外安装。
- CLI 是 `toy-device-simulator/cmd/simctl`，在 `toy-device-simulator/` 下 `go run ./cmd/simctl …`，不用预装。
- `go run ./cmd/simctl up` 编译并后台启动 manager。设备册为空时会顺手建一台 `sim_0001`（身份字段取默认产品），之后就能 `run`。

## 3. 本机自己准备的部分（不入库）

**配置树**：复制 `configs/registry.yaml` 为 `configs/registry.local.yaml`（已被 .gitignore 挡住），在里面加你要测的环境、厂商、设备类型，之后一直用 `up --registry configs/registry.local.yaml` 启动。manager 已在跑时也可以用 `/registry` 接口加（见 SKILL.md）。加过的条目留着。

**音频素材**：素材库在 `toy-device-simulator/data/assets/`，新机器上是空的。从调试台素材库或 `POST /assets` 导入，**每条都打内容标签**（`对话`、`联网`、`故事`、`识图` 等），名字写清楚说的是什么。来源不限：录音、语音合成都行。素材前后各留约 0.6 秒、1.2 秒静音，整条 2.5 秒以上，太短服务端可能拿不到识别结果。

测拍照识别还要：

- 一条问图的话，打 `识图` 标签，例如「请看一下这张图片，上面写的是什么？」。
- 一张白底大字的 jpg 图片，字选猜不中的词（例如「紫色的大象」），识图回复才能逐字核对。用任意画图或图片生成手段做都行。

在 Windows 命令行里传中文字段（素材名、图片上的字）容易被转码弄坏：先写进 UTF-8 文件，再从文件读进请求。

## 4. 自检

```text
go run ./cmd/simctl context
```

`alive=true`、`devices` 至少一台、`registry` 里有你要的环境、`audio_tags` 有你要的标签，就可以开始。不连真实服务端时，可以起本地回环服务端 `go run ./cmd/echosrv`，用配置树里的「本地 / demo / A3」验证链路。
