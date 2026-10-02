# 拍照识别

**拍照识别**走指令拍照：问句被服务端决策交给 Camera 技能 → 下发拍照指令（`command/client`，`movement.behavior=601`，带 QuestionKey 与 `start_text` / `start_voice`）→ 设备按产品的拍照功能传图 → 服务端识图（imageAnalysis）→ 回一段 UUID=0 的语音，算本轮回复。

## 备料

- **图片资产**：白底大字的 jpg，字选猜不中的词（如「紫色的大象」），识图结果才能逐字核对。现成的在 `context` 的 `images` 里（或 `assets --kind image`）。
- **问图话术**：让决策走 Camera 的问句，如「请看一下这张图片，上面写的是什么？」。素材库里打了 `识图` 标签的就是，`--tag 识图` 直接选中。

库里没有时按 [setup.md](setup.md)「本机自己准备的部分」备齐：图片用任意手段做，导入走 `POST /assets`。Windows 命令行里的中文字段先写进 UTF-8 文件再从文件读，直接写在参数里会被转码弄坏。

## 跑

```text
go run ./cmd/simctl run <device_id> --env <环境名> --enterprise <厂商简称> --device-type <类型简称> --product default --tag 识图 --set audio.format=amr --set behavior.downlink_idle_timeout_sec=6 --set features.photo.enabled=true --set features.photo.image=<图片资产 id>
```

- 拍照功能来自产品：产品里已开拍照并配好图，就不用最后两个 `--set`。它们和其它覆盖一样只在 start 时生效，设备在跑加 `--restart`。
- 图由设备收到拍照指令后自动传，命令里只配 `features.photo`；`--image` 是另一条路（见文末「带图送话」）。
- `downlink_idle_timeout_sec=6`：默认 2 秒会把分段到达的回复截短。
- 实测通过：`--env 杭州 --enterprise XYMH --device-type MH6W`（2026-09-23、2026-10-02，`photo.result=ok`）。XYMH / MH6W 这一级只在本机配置树里（不入库，新机器按 [setup.md](setup.md) 自己加），类型没配默认产品，所以带 `--product default`。`测试`/A3-TEST 不回话，`硅谷-经XYMH`/MH6W-EN 不下发拍照指令，都测不了。

## 读结果

看 `photo.result`，CLI 已把指令、传图、回复归成一个结论：

| `photo.result` | 说明 |
|---|---|
| `ok` | 指令到了、图传了、有识图语音回复。再转写核对图上的字才算通过 |
| `no_reply` | 图传了，等识图回复超时（`features.photo.reply_timeout_sec`，0 = 60 秒），或只收到提示音 |
| `skipped:<原因>` | 设备没传图：`功能没开` / `没配图` / `读图失败` / `组帧失败` |
| `not_uploaded` | 指令到了但没传图也没跳过记录，查 `turn` 事件 |
| `no_command` | 拍照开着，服务端没下发拍照指令：问句没交给 Camera，或设备类型没配 Camera 指令 |
| `image_sent` | `--image` 带图送话发出了（见文末） |

`turn` 的事件依次是 `vad` → `command_received`（`movement=601`）→ `photo_command`（reason 是 QuestionKey）→ `photo_uploaded`（`source=command`）→ `tts_done` → `turn_terminal`。下行里 UUID=0 的音频是识图回复；本轮 UUID 的那一包（配了提示音的机型才有）是断句提示音。

- **转写**：`simctl audio <device_id> --instance <id> --turn <id> --side downlink --out reply.wav` 取回复，用手边的语音转写手段转成文字，对照图上的字。没有转写手段时如实报告「未核对内容」，不要只凭 `photo.result=ok` 宣布通过。
- **留档**：本轮目录里 `photo_<uuid>.<格式>` 是实际传出的图（uuid 取自 `photo_uploaded` 的 reason），`photo_start_voice.<格式>` 是指令带的开场语音。也可以 `GET /devices/{id}/turns/{turn_id}/photo?instance_id=…&uuid=…` 取图。
- 服务端不回传图 ack，传成功与否只看后续回复。

## 带图送话（另一条路）

`--image <图片资产 id>`：每轮先用本轮 UUID 传图（QuestionKey 为空）再说话；服务端设备类型开了 `imageChat` 才会用这张图，回复是本轮的普通 TTS（`reply_kind=tts`）。音频集的每条都带；不看产品的拍照开关。

- 问句被决策交给 Camera 时（问「看图」多半如此）走的仍是指令拍照，`--image` 传的图被服务端忽略。
- 结果回显 `image_asset_id`；`photo.result=image_sent` 就是带图送话发出了。模拟器这边能证明的只到「图先于音频、同一 UUID 发出」：`photo_uploaded`（`source=speak`）reason 里的 `uuid` 等于 turn 的 `uplink_uuid`。
- 回复没提到图，依次查服务端：设备类型的 `imageChat`；模型是不是 gemini / 阿里自定义应用 / 火山 bot、chatmix；图是不是 jpg；日志有没有 `ImageURL is empty`（音频很短时图可能还没就绪）。simctl 看不到服务端配置，报告里写明「未确认服务端 imageChat」。
