# Phase 7 设备定义持久化与设备管理

## 0. 为什么

这个模拟器最终是**给 agent 做测试**用的：agent 按 `device_id` 引用一台已经配好的设备，
启动、送话、断言服务端行为。而设备此前**只活在 manager 内存里**——进程一停全丢，
agent 每次都得重建。配置树、模板、音频库都落盘，唯独设备不落，这是个洞。

同时，人在调试台上手改参数（「把这台从 mp3 切到 amr 试一发」）**不应该污染 agent 要用的基线**。
所以本阶段拆出两层，并给定义一个独立的管理界面。

## 1. 两层模型

| | 是什么 | 存在哪 | 谁改 |
|---|---|---|---|
| **定义**（`def` / `defEnv`） | 落盘基线，agent 按 `device_id` 引用的就是它 | `data/devices.yaml` | 设备管理界面 / `PUT /definition` |
| **当前值**（`cfg` / `envName`） | 本次运行实际用的配置 | manager 内存 | 调试台配置抽屉 / `PUT /config` |

- 创建时两者相同。
- `PUT /config` **只改当前值、不落盘**——manager 重启即回到定义。
- `PUT /definition` 改定义并落盘；**不看运行状态**（定义下次 start 才生效，不影响正在跑的这一轮），
  但 `created` / `stopped` 时顺手把当前值拉齐，否则「改完定义、一启动还是旧值」很反直觉。
- `POST /config/reset` 丢弃临时修改，当前值回到定义。

## 2. 落盘

`data/devices.yaml`，跟随 `assets_root` 的父目录（测试只要换 `assets_root` 就自动隔离，
不必再加一个配置项）。

- **存定义不存运行状态**：加载回来一律 `created`，不自动 start。
- 落盘发生在三处：创建（单台与模板批量）、`PUT /definition`、删除。`PUT /config` 不落盘。
- 加载时运输层参数（`write_queue_depth` / `write_drain_timeout_sec`）**以当前 `manager.yaml` 为准**，
  不用盘上的旧值；单条定义校验不过就跳过该条，不阻止 manager 启动——调试台不该因为
  一台设备的脏定义整个起不来。
- 格式用 YAML 而非 JSON：`config.Device` 只有 yaml tag，且 `Behavior` 用 `*bool` 区分
  「省略」与「显式 false」，yaml 能原样往返；换 JSON 要给 6 个结构体补一整套 tag。

## 3. 新增与变更的端点

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/devices/{id}/definition` | 读定义 |
| `PUT` | `/devices/{id}/definition` | 改定义并落盘；无运行态门禁 |
| `POST` | `/devices/{id}/config/reset` | 当前值 ← 定义；**Running 返回 409**（同 `PUT /config`：运行中换采样率/格式会让已发出的帧头与等待预算对不上） |
| `PUT` | `/devices/{id}/config` | **语义变更**：不再落盘 |
| `GET` | `/devices/{id}/config`、`/definition` | 响应增 `overridden` |
| `GET` | `/devices` | 每行增 `audio{format,sample_rate,bitrate_kbps}` 与 `overridden`，管理表格不必为每行再打一次 `GET /config` |

`PUT /config` 与 `PUT /definition` 共用 `patchConfigLocked`，差别只在运行态门禁与是否落盘。

**`overridden` 的准确含义是「当前值 ≠ 定义」**，成因有二：在配置抽屉里临时改了，
或者**运行中改了定义**（定义前移、当前值没动）。界面上标作「已临时改」是前一种的说法，
agent 判断「这台是不是干净基线」两种都该当作不干净，所以这个布尔够用。

## 4. 界面

**顶栏**多一组「调试台 / 设备管理」切换。它排在「中栏形态」右侧贴分隔线——
切到设备管理会隐藏中栏形态那一组，视图切换若排在左边就会被右对齐布局往右拽，
刚点完的按钮从光标底下跑掉。

**设备管理**是全屏表格：`device_id`、三级挂靠、音频格式 / 采样率 / 码率、播放模式、
实例状态、**定义**列（`overridden` → 「已临时改」/「一致」）、操作。

- 每行：调试（跳回调试台并选中）/ 编辑定义 / 复制 / 删除（两击确认）
- 勾选出批量条，复用现有 `/devices/batch/{start,stop,delete}`
- **编辑定义复用配置抽屉的表单渲染**，靠一个 `def` 参数切换目标与提交端点：
  定义模式不受运行态门禁，提交按钮写明「保存到定义（落盘）」。20 多个字段、
  树挂靠级联、校验提示都是现成的，没有第二套。
- 复制走 `GET definition` → `POST /devices`，id 自动取 `{id}_copy` / `_copy2`……
  `device_id` 不可变，只能一次给对，所以不弹对话框问。

**配置抽屉**多一个「重置为定义」按钮（`overridden` 时可用），并在打开时重拉一次配置——
`overridden` 可能被别处改过（agent、另一个页签、本页的重置），拿选中设备时的缓存
会让按钮状态不对。

**新建表单**补上 `audio.format` 下拉与 `audio.bitrate_kbps`。此前新建只能建 pcm 设备
（`defaultDevice` 硬编码），要一台 mp3 设备得「建完 → 开配置抽屉 → 改格式 → 保存」三步。

## 5. 已知取舍

**重启后旧 instance 的录音取不到。** `instance_id` 是**每进程新生成**的
（`POST /devices` 时分配一次，stop/start 只换 `conn_generation`），而录音与事件
按 instance 分区（`recordings/{device_id}/{instance_id}/{turn_id}/`，查询必填 `instance_id`）。
设备持久化后，manager 重启会给同一台设备换一个新 `instance_id`，**旧目录仍在盘上但
API 返回 404 `instance 未命中`**。

没有一起解决，因为两条修法都在动已经稳定的 instance 世系：启动时扫盘把旧 instance
注册成 tombstone，要重新定义 tombstone TTL 语义（现在是删设备后才进墓碑）；
让设备沿用 `last_instance_id`，则两次运行的事件游标会串在一起。
而 agent 的用法是「跑完当场断言」，不依赖跨重启的历史。

> **已在 Phase 8 解决**（见 `phase8.md`），走的是上面两条之外的第三条：世系一个字不动，
> 改的是「一个 instance 从哪里读」——历史读端点多认一种来源，盘上有目录就能读；
> 事件也随之落盘。`/wait` 与 WS 仍只认内存。

## 边界（本阶段不做）

- **sqlite**。设备清单是整份重写、几十台的量级，一个文件够用；等到要按条件查、
  或多进程并发写再换（代码里留了 `ponytail:` 标记）。
- 定义的导入 / 导出，字段级 diff（`overridden` 只是布尔，不告诉你哪几个字段不一致）。
- 重启后旧 instance 的历史（见 §5）。**已在 Phase 8 补上。**
- 管理视图的三级筛选下拉——搜索框已经能搜环境 / 厂商 / 类型子串。
