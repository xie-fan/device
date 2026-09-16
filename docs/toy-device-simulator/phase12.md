# Phase 12：产品

**状态：计划已批准，实现以本文为准。**

## 1. 为什么

不同产品支持不同的音频格式、对话模式，部分支持拍照。Phase 11 之后这些属性仍焊在设备册条目上：同一产品建 10 台要抄 10 份，想让一台设备换成另一种产品跑，得先改它的定义。

本期把属性整体交给**产品**，设备只剩 `device_id`，start 时选产品：

| | 内容 | 存在哪 | 什么时候定 |
|---|---|---|---|
| **设备册条目** | 只有 `device_id` | `data/devices.yaml` | 建设备时 |
| **产品** | 全部设备属性（含身份字段）、对话模式与音频格式的支持清单、功能 | `data/products.yaml` | 产品管理 |
| **挂靠** | 环境 / 厂商 / 设备类型 | 内存 | start 时 |
| **临时覆盖** | 按字段的差量 | 内存 | start 时带上，或 `PUT /config` |

实例的运行配置 = 产品默认值 ← 临时覆盖 ← 挂靠（`bindDevice`）。

术语见 `CONTEXT.md`；取舍见 `docs/adr/0001`、`0002`。

## 2. 产品

### 2.1 形状

YAML（`data/products.yaml`）：

```yaml
products:
  - id: default
    name: 默认产品
    playing_modes: [1, 2, 3]
    audio_formats: [pcm/16000, wav/16000, mp3/16000, amr/16000, aac/16000]
    defaults:          # 与 config.Device 同形；不得带 device_id / enterprise / device_type / server
      action: chatbot
      firmware_version: 1.0.0
      nic_type: wifi
      nic_iccid: "8986xxxxxxxxxx"
      playing_mode: 1
      audio: {format: pcm, sample_rate: 16000, channels: 1, sample_format: s16le, slice_ms: 100, max_payload_size: 51200}
      behavior: {…}
      uuid: {min: 1, max: 2147483647}
      recording: {enable_frame_log: true, save_uplink_audio: true, save_downlink_audio: true, output_dir: ""}
      features:
        photo: {enabled: false, image: "", server_default_reply: false, slice_interval_ms: 0, reply_timeout_sec: 0}
```

JSON（REST）：

```json
{"id": "default", "name": "默认产品", "playing_modes": [1,2,3], "audio_formats": ["pcm/16000", "…"],
 "defaults": {"action": "chatbot", "playing_mode": 1, "firmware_version": "…", "nic_type": "…", "nic_iccid": "…",
              "audio": {…}, "uuid": {…}, "behavior": {…, "downlink_ack": {…}}, "recording": {…},
              "features": {"photo": {"enabled": false, "image": "", "server_default_reply": false,
                                     "slice_interval_ms": 0, "reply_timeout_sec": 0}}}}
```

`defaults` 的字段名与 `GET /devices/{id}/config` 一致，去掉 `environment / enterprise / device_type / device_id / server`，加上 `features`。

Go：

```go
// config
type Product struct {
    ID           string   `yaml:"id"`
    Name         string   `yaml:"name"`
    PlayingModes []int    `yaml:"playing_modes"`
    AudioFormats []string `yaml:"audio_formats"` // "格式/采样率"
    Defaults     Device   `yaml:"defaults"`
}
type Features struct {
    Photo PhotoFeature `yaml:"photo"`
}
type PhotoFeature struct {
    Enabled            bool   `yaml:"enabled"`
    Image              string `yaml:"image"`                // 图片资产 id
    ServerDefaultReply bool   `yaml:"server_default_reply"` // true：Reserved 留空，服务端按默认回 aac
    SliceIntervalMs    int    `yaml:"slice_interval_ms"`    // 0 = 50ms
    ReplyTimeoutSec    int    `yaml:"reply_timeout_sec"`    // 0 = 60s
}
// Device 增加：Features Features `yaml:"features"`

func DefaultProduct() Product                          // id=default，值见 2.1 的 YAML
func ValidateProduct(p Product) error
func ParseAudioSpec(s string) (format string, sampleRate int, err error)
```

`DefaultProduct().Defaults` 的 `recording.output_dir` 为空（合成时补 RecordingsDir），`write_queue_*` 为 0（合成时注入）。

### 2.2 校验（`ValidateProduct`）

- `id` 过 `ValidatePathComponent`；`name` 去空白后非空。
- `playing_modes` 非空，每项 1..3，不重复。
- `audio_formats` 非空，每项过 `ParseAudioSpec`（格式在 `media.TargetFormats`、采样率为正整数、恰好一个 `/`），amr 只许 8000/16000，不重复。
- `defaults` 的 `device_id / enterprise / device_type / server.url` 必须为空；其余按设备属性规则校验（同 `validateAttrs`；`write_queue_*` 不在产品里，校验时视为合法）。
- 默认值必须在清单内：`defaults.playing_mode ∈ playing_modes`，`defaults.audio.format/sample_rate` 在 `audio_formats` 里。
- `features.photo.slice_interval_ms`、`reply_timeout_sec` ≥ 0。开了拍照但没配图是合法的。

### 2.3 存储

- 路径跟随设备册：`dir(assets_root)/products.yaml`。
- `manager.LoadProducts(path)`：文件不存在 → 写入只含 `config.DefaultProduct()` 的文件；解析失败或任一产品校验不过 → 返回错误（manager 起不来，同配置树）。
- 每次变更整份原子重写（同 `Registry.saveLocked`）。

```go
func LoadProducts(path string) (*Products, error)
func (p *Products) List() []config.Product            // 按 id 排序
func (p *Products) Get(id string) (config.Product, bool)
func (p *Products) Add(prod config.Product) error      // 校验失败 → 普通错误；重复 id → ErrRegistryConflict
func (p *Products) Update(id string, prod config.Product) error // prod.ID 为空沿用 id；非空且 ≠ id → 普通错误；不存在 → ErrRegistryNotFound
func (p *Products) Delete(id string) error             // 不存在 → ErrRegistryNotFound
```

### 2.4 端点

| 方法 | 路径 | 成功 | 失败 |
|---|---|---|---|
| `GET` | `/products` | 200 `{"products":[…]}`（按 id 排序） | |
| `GET` | `/products/{id}` | 200 产品 | 404 |
| `POST` | `/products` | 201 产品 | 400 校验；409 重名 |
| `PUT` | `/products/{id}` | 200 产品 | 400 校验、body 的 `id` 与路径不符；404 |
| `DELETE` | `/products/{id}` | 204 | 404；被设备类型设为默认产品 → 409；被 starting/running 设备使用 → 409 |

- POST / PUT body：`id`（PUT 可省）、`name`、`playing_modes`、`audio_formats`、`defaults`。
- `defaults` 里省略的属性按 `config.DefaultProduct().Defaults` 补齐。PUT 是整份替换，同样补齐，不在旧值上打补丁。
- `defaults` 的字段检查与 `PUT /config` 同一套：未知字段、`write_queue_*`、`auto_register/auto_report=false`、`device_id / enterprise / device_type / server` → 400。

### 2.5 设备类型的默认产品

- `manager.DeviceType` 增 `DefaultProduct string`（yaml `default_product,omitempty`，json `default_product`）；`GET /registry` 的类型节点带出。
- `POST …/device_types` 与 `PUT …/device_types/{tshort}` 的 body 可带 `default_product`：
  - PUT 时**字段出现才改**，`""` 清除；不出现不动。
  - 非空时产品必须存在，否则 404；POST 时产品不存在则不建类型。
- 改类型名称或简称不影响默认产品。

```go
func (r *Registry) SetDeviceTypeDefaultProduct(envName, entShort, typeShort, product string) error // 节点不存在 → ErrRegistryNotFound
func (r *Registry) DefaultProduct(envName, entShort, typeShort string) (string, error)
func (r *Registry) ProductReferences(product string) []string // 每项 "环境/厂商简称/类型简称"
```

## 3. 设备册

- `POST /devices`：
  - `{"device_id":"x"}` → 201 `{"device_ids":["x"],"instances":[{"device_id","instance_id"}]}`。
  - `{"id_prefix":"sim","count":N}` → 201，id 为 `sim_1…sim_N`；任一冲突整批 409，一台都不建。
  - 带 `device`、`template_id`、`environment`、`enterprise`、`device_type` 任一 → 400；缺 id、id 非法、`count ≤ 0` → 400；重名 → 409。
- `data/devices.yaml` 写成 `devices: [{device_id: x}, …]`；读时兼容旧形状 `devices: [{device: {device_id: x, …}}]`，只取 id（不写迁移代码）。
- **删除**：模板（`/templates*`、`--templates`、`TemplatesDir`、`configs/templates/`）；`GET/PUT /devices/{id}/definition`。

## 4. 启动与临时覆盖

### 4.1 start

```
POST /devices/{id}/start
{"environment": "…", "enterprise": "…", "device_type": "…",
 "product": "mh8w",
 "overrides": {"audio.format": "mp3", "behavior.first_reply_timeout_sec": 7}}
```

- 三级规则同 Phase 11。
- `product` 省略 → 用设备类型的 `default_product`；类型没配 → 400；显式给的产品不存在 → 404；默认产品指向不存在的产品 → 400。
- 设备上次用的产品非空且与这次不同 → 先清空覆盖。
- `overrides` 并入覆盖（同一字段后到的赢）。
- 合成：产品 `defaults` ← 覆盖 → 写 `device_id`、注入 `write_queue_*`（manager.yaml）、`recording.output_dir` 为空时补 RecordingsDir → `bindDevice` → `ValidatePhase2`。**任何一步失败返回 400，设备的状态、产品、覆盖都不变。**
- 202 响应增 `product`。
- `POST /devices/batch/start` 与场景 `batch_start` 步骤同样接受 `product`、`overrides`（整批共用）。

### 4.2 覆盖的键

- 点路径，名字与 `GET /config` 的字段一致：`playing_mode`、`action`、`firmware_version`、`nic_type`、`nic_iccid`、`audio.format`、`audio.sample_rate`、`audio.bitrate_kbps`…、`uuid.min`、`behavior.first_reply_timeout_sec`、`behavior.downlink_ack.mode`、`recording.output_dir`、`features.photo.enabled`、`features.photo.image`…。值是 JSON 标量。
- 未知路径，以及 `device_id`、`environment / enterprise / device_type`、`server.*`、`product`、`behavior.write_queue_depth`、`behavior.write_drain_timeout_sec` → 400。
- **不受产品清单限制**：`audio.format` 覆盖成清单外的值照常启动。
- 与产品默认值相等的字段不计入覆盖。
- 覆盖只活在内存里，manager 重启即清空。

### 4.3 `PUT /devices/{id}/config`

- body 仍是嵌套 JSON，按点路径并入覆盖，再重新合成当前值。
- 检查顺序：
  1. 静态检查 → 400：`write_queue_*`、`device_id`、未知字段、`auto_*=false`、挂靠三键、`product`。
  2. 设备在本进程里还没选过产品 → 409。
  3. 运行中改禁改字段 → 409；`features`、`recording` 运行中可改。
- 运行中改 `features` 立即对正在跑的实例生效（下一次收到拍照指令时读）。
- 响应：当前配置 + `product` + `overrides` + `overridden`。

### 4.4 reset

`POST /devices/{id}/config/reset`：清空覆盖，当前值回到产品默认值；运行中 409（不变）。没选过产品时 200，没有东西可清。

### 4.5 视图

- `GET /devices` 每行、`GET /devices/{id}`、`GET /devices/{id}/config` 增 `product`（没选过为 `""`）、`overrides`（对象，没有为 `{}`）；`overridden` = 覆盖非空。`GET /config` 增 `features`。
- 没选过产品的设备，`GET /config` 的属性是空值。

## 5. 历史

- `turn.json` 增 `product`、`overrides`（本轮生效的覆盖）。
- `GET /devices/{id}/instances` 每项增 `product`（取首个 turn）。

## 6. simctl

- `run` 增 `--product P` 与可重复的 `--set 路径=值`（值先按 JSON 解析，失败当字符串），随 start 请求发出。
- 设备停着：有覆盖先 reset 再 start（同 Phase 9）。
- 设备在跑（running / starting）：
  - 有覆盖且与本次 `--set` 不同 → 报错，`--dirty` 放行；相同 → 复用。
  - 挂靠、产品、`--set` 都不生效，也不报错（维持 Phase 11 的静默复用）。结果里的 `product`、`overrides` 是设备实际值，据此核对。
- `run` 结果每项增 `product`、`overrides`、`photo`（见 8.4）。
- 新动词 `products`：原样输出 `GET /products`。
- skill 文档写明：换产品或改 ICCID 会让真实服务端重新校验这台设备。

## 7. 素材库图片

- `POST /assets` 按魔数识别 jpg（`FF D8 FF`）、png（`89 50 4E 47 0D 0A 1A 0A`）、bmp（`BM`）→ `kind:"image"`，`format` 为 `jpg|png|bmp`；其余照旧 → `kind:"audio"`。旧索引缺 `kind` 按 audio。
- 列表与单个资产的响应带 `kind`；`GET /assets?kind=image` 只列图片。
- 图片资产不能用于 speak / stream → 400，错误信息写明是图片资产。
- `GET /assets/{id}/content` 对图片返回原字节，Content-Type 为 `image/jpeg|image/png|image/bmp`，忽略 `decode`。
- start 时 `features.photo.image` 非空，必须是存在的图片资产，否则 400。

## 8. 拍照功能

依据真实服务端（ai-creates-wealth）：`service/skills/camera.go`、`mqtt/controller/uploadImageFinish.go`、`module/imageModule/analysis.go`、`common/types/newProtocol.go`。

### 8.1 触发

收到 `'1'` 管理消息，topic 以 `/command/client` 结尾，且 `data.movement.behavior == 601`、`data.data.QuestionKey` 非空：

- 记事件 `photo_command`（reason 为 QuestionKey）。需要 ACK 的照旧 ACK。
- `data.movement.start_voice` 非空 → base64 解码，存为本轮目录下 `photo_start_voice.<audio.format>`（无活跃轮不存），不算本轮 TTS。
- 功能没开、没配图、读图失败 → 记 `photo_skipped`（reason 写原因），不传图。

### 8.2 上传帧

每片：`'2'` + 100 字节小端 `ImageHeader` + 分片数据。

| 字段 | 偏移 | 值 |
|---|---|---|
| Head | 0 | `0x5050` |
| Stage | 4 | 非末片 1；末片 2，**末片必须带数据** |
| SequenceNumber | 8 | 等于 SliceIndex |
| UUID | 12 | 随机 1..0x7FFFFFFF，一次上传共用 |
| Total | 16 | 1 |
| TotalSize | 20 | 图片总字节数 |
| SliceTotal | 24 | 片数 |
| SliceIndex | 28 | 从 0 开始 |
| SliceSize | 32 | 本片字节数 |
| QuestionKey | 36..51 | 指令里的 QuestionKey，ASCII，不足补 0 |
| ImageFormat | 52..59 | 资产格式 `jpg|png|bmp`，补 0 |
| Reserved | 60..99 | `server_default_reply=false`：实例 `audio.format`，补 0；`true`：全 0 |

每片载荷 ≤ 51200 字节；片与片之间间隔 `slice_interval_ms`（0 → 50ms）。发完记 `photo_uploaded`（reason 写字节数与片数）。服务端不回 ack。

### 8.3 轮次

指令到达时有活跃轮（Speaking / FinishingUpload / WaitingReply）：

- 本轮进入「等拍照回复」：不再按「只收到指令」的 followup 收尾，改用 `reply_timeout_sec`（0 → 60s）计时。
- 期间 UUID=0、Stage=1 的下行音频按本轮 TTS 处理：`tts_chunk` 事件、`down_format`、`down_bytes`、存下行音频、下行空闲计时。
- 计时到期仍没收到 → 按「只收到指令」收尾，与 followup 到期相同：`turn_end_reason=idle`、`reply_kind=command`。

无活跃轮时只传图、记事件，不开新轮；UUID=0 的下行照现状丢弃。

### 8.4 simctl 的 photo 摘要

`run` 结果的 `photo`：`{"command": bool, "uploaded": bool, "skipped": "原因或空串"}`，取自本轮事件。判语不变：拍照后收到语音 → `replied`；没收到 → `reply_kind=command` 归为 `replied_no_audio`。

## 9. UI（功能清单，视觉交给 Claude Design）

- 产品管理页：列表、新建、编辑、删除（被引用时显示原因）；编辑支持清单、默认值、拍照功能。
- 配置树的类型节点：默认产品下拉。
- 挂靠条：产品下拉，默认取所选类型的默认产品；启动时带上。
- 新建设备：只填 id，或前缀＋数量。设备管理视图去掉编辑定义与复制。
- 配置抽屉：显示产品默认值与覆盖，覆盖字段标「已临时改」；换产品时提示覆盖会清空；拍照功能开关（运行中可改）；「播放模式」改名「对话模式」。
- 身份字段旁提示：换产品或改 ICCID 会让真实服务端重新校验这台设备。
- 素材库：图片导入、预览、按类型筛选；拍照用图从图片资产里选。
- 修：配置抽屉在设备未启动时保存必 400；「单轮语音回归」场景的 batch_start 缺挂靠。

## 10. 迁移

- `data/devices.yaml` 旧条目只留 id；`data/products.yaml` 缺失时自动生成默认产品。
- 旧属性不自动转成产品，按清单人工建。

## 11. 不在本期

- 服务端下发 `command.playingMode` 时模拟器跟着切换。
- 设备主动传图（imageChat 带图对话）；echosrv 模拟拍照。
- 覆盖落盘；产品导入导出。
