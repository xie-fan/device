# Phase 14：带图送话与传图留档

**状态：已实现，实现以本文为准。真实服务端的手工冒烟（§9 末条）待做。**

## 1. 为什么

Phase 12 只做了**指令拍照**：服务端下发拍照指令，设备传图，图片分析的语音回复算本轮。真实服务端还有第二条路，模拟器目前走不了：

- **带图送话**：设备先传一张图，再说话，图和音频用同一个 UUID。服务端的设备类型开了 `imageChat`，大模型就把图和问题一起答（「这是什么」）。
  模拟器走不了：`BuildImageFrames` 拒绝空 QuestionKey，送话也没法带图。
- **传出的图没有留档**：`photo_uploaded` 只记字节数和片数。回看时不知道传的是哪张；资产改了或删了，就再也找不回当时的图。

两条路对照：

| | 指令拍照（Phase 12） | 带图送话（本期） |
|---|---|---|
| 谁发起 | 服务端：`command` 下发 `movement.behavior=601` | 设备：送话时带一张图 |
| QuestionKey | 指令里给的，非空 | 空 |
| 图的 UUID | 随机 | 等于本轮音频的 UUID |
| Reserved（回复格式） | 实例 `audio.format`，或全 0 用服务端默认 | 全 0（服务端不读） |
| 服务端怎么用图 | 用 QuestionKey 取回问题，做图片分析 | 存 redis `{device_id}_{uuid}`；大模型按本轮 UUID 取图 |
| 回复 | UUID=0 的 TTS，算本轮 | 普通 TTS，UUID 等于本轮 |
| 模拟器里怎么触发 | 产品的 `features.photo`（开关 + 拍照用图） | 送话请求带 `image_asset_id` |

术语见 `CONTEXT.md` 的「指令拍照」「带图送话」。

## 2. 服务端行为

依据真实服务端（ai-creates-wealth master）：`websocket/controller/handle.go` 的 `ImageHandle`、`module/camera/image.go` 的 `Slice`、`mqtt/controller/uploadImageFinish.go` 的 `UploadImageFinishOnlyUpload`、`module/voiceModule/turn/turn_stage_dialogue.go` 的 `generateDialogueReply`、`service/cloud/google/ai/gemini.go` 的 `StreamChatWithImage`。

1. 末片（Stage=2）的 QuestionKey 为空：服务端合并分片、传 OSS，把 URL 存进 redis，键 `{device_id}_{uuid}`，10 分钟过期。WS 上不回 ack。
2. 语音轮：设备类型配置 `imageChat=true` 时，服务端把本轮 UUID 放进上下文；大模型层按 `{device_id}_{uuid}` 取图，和识别文本一起送进模型。取不到只打一行日志 `ImageURL is empty`，照常回答。
3. 会取图的大模型路径：gemini、阿里自定义应用、火山 bot / chatmix。gemini 把 MIME 写死成 `image/jpeg`。

模拟器管不了的前提：设备类型开 `imageChat`；大模型走上面几条路径之一；测试图用 jpg。

服务端已知缺陷（不在模拟器里修）：服务端每收到一片就起一个 goroutine 处理。末片如果先于前面的分片落盘，`CheckImageSlice` 发现缺片，接着执行 `time.Sleep(time.Millisecond * delay)`，而 `delay` 本身已是 100ms，实际睡约 28 小时，这张图等于丢了。设备在片与片之间留间隔（模拟器默认 50ms）能降低概率，消除不了。

## 3. 带图送话

### 3.1 请求

`POST /devices/{id}/speak` 与 `POST /devices/{id}/speak_and_wait` 的 body 新增可选字段 `image_asset_id`：

```json
{"asset_id": "ast_…", "image_asset_id": "ast_…"}
```

- 与 `asset_id`、`stream` 都能搭配；压缩格式设备同样支持。
- 资产不存在 → 404 `image_asset_id 不存在`；不是图片资产 → 400 `image_asset_id 不是图片资产`。
- 受理时就读好图的字节。之后改、删这张资产，不影响已受理的这一轮，排队中的也一样。
- 不看 `features.photo.enabled`：调用方显式带了图，就传。
- 响应不变。

### 3.2 上行顺序

带图的一轮，上行协程在发第一帧音频之前先传图。pcm/wav 预构建与压缩格式流式两条路径一样：

1. 用本轮 UUID 组图片帧：QuestionKey 全 0，ImageFormat 为资产格式，Reserved 全 0，Total=1，其余字段同 phase12 §8.2。
2. 片间隔用 `features.photo.slice_interval_ms`（0 → 50ms）。
3. 每片发之前做与音频循环相同的检查：本轮已不是当前轮、已终态、已冻结或正在收口，就停止，不再发剩下的分片；音频循环看到同一状态会直接退出。
4. 发完记 `photo_uploaded`（§5），然后照原样发音频，Seq 从 0 开始。

- 传图的时间不计入首包回复超时：首包超时仍在音频发完后开始计时。
- 组帧失败理论上不会发生（受理时已校验）。万一发生，记 `photo_skipped`，reason 为 `组帧失败`，照常发音频。

### 3.3 轮次与判语

不变。带图送话的回复是普通 TTS，UUID 等于本轮，走现有的完成矩阵。

Go：

```go
// protocol：questionKey 允许为空；其余校验不变
func BuildImageFrames(img []byte, format, questionKey, replyFormat string, uuid uint32) ([][]byte, error)

// core
type Photo struct {
    AssetID string // 只进事件 reason
    Format  string // jpg|png|bmp
    Data    []byte
}
// 多一个 photo 参数，nil = 不带图；Speak(pcm) 不变
func (d *DeviceInstance) SpeakPermit(pcm []byte, photo *Photo, tryAcquire func() bool) (SpeakResult, error)
func (d *DeviceInstance) SpeakStreamPermit(open StreamOpen, durMs, chunkBytes int, photo *Photo, tryAcquire func() bool) (SpeakResult, error)
```

## 4. 传图留档

- 两条路径都存：`<本轮目录>/photo_<uuid>.<jpg|png|bmp>`，内容是实际发出的字节。
  - uuid 取这次传图 ImageHeader 里的 UUID：带图送话等于本轮 `uplink_uuid`；指令拍照是那次上传的随机 UUID。
  - 一次上传一个文件，同一轮收到两次拍照指令也不会互相覆盖。
- 受 `recording.save_uplink_audio` 控制：关掉就不存（压测时省盘）。
- 指令拍照到达时没有活跃轮，就不存（同 `photo_start_voice`）。

新端点：

| 方法 | 路径 | 成功 | 失败 |
|---|---|---|---|
| `GET` | `/devices/{id}/turns/{turn_id}/photo?instance_id=…[&uuid=N]` | 200 图片原字节，Content-Type `image/jpeg\|image/png\|image/bmp` | 400 缺 `instance_id`、`uuid` 非法；404 instance 或 turn 未命中、照片不存在 |

- 省略 `uuid` 就取本轮 `uplink_uuid` 对应的那张，也就是带图送话传的图。指令拍照传的图，uuid 从 `photo_uploaded` 的 reason 里取。
- 盘上历史照样能取，实例解析同 `audio/uplink`。

## 5. 事件

`photo_uploaded` 的 reason 两条路径统一成：

```
source=speak asset=ast_… uuid=123 bytes=204800 slices=4
source=command asset=ast_… uuid=456 bytes=204800 slices=4
```

- Phase 12 的旧形状 `bytes=… slices=…` 不再产出；老 `events.jsonl` 里的保持原样。
- `photo_command`、`photo_skipped` 不变。

## 6. simctl

- `run --image <图片资产 id>`：本次 run 的每一轮都带这张图，`--audio-set` 的每一条也带。
- 跑成的结果项新增 `image_asset_id`，没带图为 `""`。出错的元素形状不变（`device_id` / `asset_id` / `error`）：一次 run 只有一张图，报错信息里已写明是图的问题。
- `photo.uploaded` 表示本轮传过图，两条路径都算。`command=false`、`uploaded=true` 就是带图送话。
- 不带 `--image` 时行为不变。
- skill 的 run.md 补一段排查：回复没提到图里的东西，依次查设备类型的 `imageChat`、大模型路径、图是不是 jpg、服务端日志里有没有 `ImageURL is empty`。

## 7. 调试台（功能清单，视觉交给 Claude Design）

已并入 `docs/ui-redesign-brief.md` §10（I1–I4）。

- 送话条：「带图」下拉，列素材库里的图片，默认「不带」。选了就随每次送话带上；跑音频集时每条都带。
- 气泡：带图的轮显示缩略图（`GET …/photo`），点开看原图。指令拍照的轮，在「指令 → 传图 → 回复」那一行也能点开看传出的图。
- 事件行：`photo_uploaded` 显示 source（带图送话 / 指令拍照）。
- 事件说明与「一个 turn 的生命周期」补上带图送话：传图 → 上行推包 → asr_result → tts_chunk ×N → turn_terminal。

## 8. 文档

- `docs/toy-device-websocket-protocol.md`：补 `'2'` 上行一节：ImageHeader 字节布局、两条路径、§2 的服务端行为与缺陷；首段「不包含」去掉拍照 `'2'`。
- `CONTEXT.md`：「指令拍照」「带图送话」；「轮」补一句「带图送话的一轮从传图开始」。
- `docs/toy-device-simulator/README.md`：Phase 14 一行与文件表。

## 9. 验收

- protocol：空 QuestionKey 能组帧，QuestionKey 与 Reserved 全 0。
- core：
  - 带图的一轮，出站顺序是全部 `'2'` 帧在前，`'0'` 帧在后，`'2'` 帧的 UUID 等于本轮 UUID。
  - `photo_uploaded` 的 reason 形状符合 §5；存档文件字节等于原图。
  - 传图中途打断：剩下的分片与音频都不再发。
  - 排队的带图送话出队后，照样先传图。
- api：`image_asset_id` 404 / 400；`GET …/photo` 默认取带图送话的那张，也能按 `uuid` 取；盘上历史能取。
- simctl：`--image` 透传；结果带 `image_asset_id`。
- 真实服务端手工冒烟：设备类型开 `imageChat`，带一张 jpg 送「这是什么」，回复说得出图里的东西。

用例：`protocol/image_test.go`；`api/photo_test.go`（core 的几条也在这里，经 fakeConn 断言出站顺序）；`cmd/simctl/product_test.go`。

## 10. 不在本期

- 传图与说话之间的间隔旋钮。服务端合并分片、传 OSS 是异步的，音频很短时大模型可能先于图就绪。真遇到（服务端日志有 `ImageURL is empty`）再加 `features.photo.speak_delay_ms`。
- 一轮带多张图：服务端一个 UUID 只认一张。
- 传图中途被打断时补发图片 Stage=3（服务端的 `UploadImageBreak`）。
- 场景 `/scenarios/run` 的 speak 步骤带图。
- `frames.jsonl` 里 `'2'` 帧的 stage / seq / uuid：Phase 12 起就记成 0。
- echosrv 模拟识图：它忽略 `'2'` 帧，本地只能验帧形态与轮次。
