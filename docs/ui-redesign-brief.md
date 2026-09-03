# 调试台 UI 重设计 · 功能清单与设计简报

对象：玩具设备模拟器的调试台（Manager 内置单页，`GET /`）。
用途：把这份清单交给设计工具/设计师，重新设计界面。**功能一个不少，布局可以全推倒。**

来源：对着 `toy-device-simulator/ui/{index.html,app.css,app.js}` 与 `api/` 路由逐条核对，并在本机跑起来实测（manager 8090 + echosrv 8089）。

---

## 1. 这是个什么东西

一台**玩具设备（智能音箱/玩具）的模拟器控制台**。真实硬件不在手边时，用它伪装成设备连上语音服务端，走完整的 WebSocket 语音对话协议，用来联调服务端。

一个操作员的实际动作循环只有四步：

```
选一台设备  →  启动（连上服务端、注册、等 Ready）  →  送一段音频上去  →  看服务端回了什么
```

其余全部是这四步的支撑：建设备、配参数、管音频素材、看协议原始事件。

**当前问题的根子**：页面最大的一块中栏叫「对话」，但里面没有对话，只有一堆控制表单；真正的对话内容（我送了什么、设备回了什么）被挤在右侧一条窄栏里。

---

## 2. 全部功能清单

### 2.1 顶栏

| # | 功能 | 说明 |
|---|---|---|
| 1 | 标题 | 「台架 · 设备调试」 |
| 2 | 事件流状态 | 5 种文案：未连接事件流 / 事件从 oldest 回放 / 只收新事件 / 事件 WS 失败 / 墓碑回放结束，WS 已关。带颜色语义（闲置·活·错·终） |
| 3 | 主题切换 | 跟随系统 → 浅色 → 深色，循环，存 localStorage |

### 2.2 左栏 · 设备名册

| # | 功能 | 接口 |
|---|---|---|
| 4 | 设备总数徽标 | — |
| 5 | 手动刷新 | `GET /devices`（另有 2 秒自动轮询，页面切到后台时暂停） |
| 6 | 搜索框 | 前端过滤 device_id / 环境 / 厂商 / 设备类型 / instance_id 子串；快捷键 `/` 聚焦 |
| 7 | 三级筛选下拉 | 环境 / 厂商 / 设备类型，级联（选项从当前设备列表派生，上级选中后下级只剩相关项） |
| 8 | 设备行 | 每行要显示：状态灯、`device_id`、`环境·厂商·设备类型`、`instance_state` 徽章、`connection_state` 徽章、error 徽章（悬停出 last_error）、`last_activity` 时刻（HH:MM:SS）。点击选中 |
| 9 | 空态 | 三种：还没有设备 / 没有匹配搜索的设备 / 没有匹配筛选的设备 |

### 2.3 左栏 · 「新建」面板

分两层：先在**配置树**上挂靠（环境 → 厂商 → 设备类型），再建设备。

| # | 功能 | 接口 |
|---|---|---|
| 10 | 环境下拉 + 新增环境 | `POST /registry/environments`；字段：环境名、url（可含 `{enterprise}` 占位符，如 `ws://127.0.0.1:8089/`） |
| 11 | 厂商下拉 + 新增厂商 | `POST /registry/environments/{env}/enterprises`；字段：名称、简称（简称是上线报文里真正用的值） |
| 12 | 设备类型下拉 + 新增类型 | `POST .../enterprises/{short}/device_types`；字段：名称、简称 |
| 13 | 挂靠不完整提示 | 三级没选全时提示 |
| 14 | 单个创建 | `POST /devices`；字段：`device_id`、`firmware_version`、`nic_type`、`nic_iccid`、`playing_mode`(1-3)、`sample_rate`。其余 30+ 个字段由前端默认值补齐 |
| 15 | 模板批量创建 | `POST /devices` + `template_id`；字段：模板（`GET /templates`）、`count`、`id_prefix`。**ID 冲突整批失败** |

> 设计要点：14 和 15 是**互斥的两种创建方式**，现在两张表单永远同时显示，是主要混乱来源之一。

### 2.4 左栏 · 「音频库」面板

全局音频素材库，与设备无关，送话时可直接引用。

| # | 功能 | 接口 |
|---|---|---|
| 16 | 素材计数 | — |
| 17 | 筛选 | 格式（pcm / wav / mp3 / amr / aac）+ 语言（自由文本，如 `zh`） |
| 18 | 素材行 | 显示：名称、格式、采样率、码率、时长、语言 |
| 19 | 试听 | `GET /assets/{id}/content`；**wav/mp3/aac 可试听，pcm 裸流与 amr 浏览器放不了，按钮需禁用并给理由** |
| 20 | 行内改名 / 改语言 | `PATCH /assets/{id}` |
| 21 | 删除 | `DELETE /assets/{id}`，两击确认（2.5 秒后自动解除） |
| 22 | 导入 | `POST /assets`（multipart）；字段：文件（wav/mp3/amr/aac）、名称、语言。上限 10 MB / 60 秒 |
| 23 | 空态 | 「库是空的，先导入一条」 |

### 2.5 中栏 · 设备工作区

#### 状态显示

| # | 功能 | 数据 |
|---|---|---|
| 24 | 标题 | 活设备是「对话」，已删除/旧实例是「历史」 |
| 25 | 状态行 | 状态灯 + `instance_state` + `connection_state` + `instance_id` 短码（悬停出全值）+ `conn_generation` + backlog 排队数 + error 徽章 |
| 26 | 故障文本 | `last_error` 全文（不能只藏在悬停里） |
| 27 | 槽占用横幅 | 「槽占用 {turn_id} · 等 turn_terminal · 队列 N 项」 |

#### 生命周期

| # | 功能 | 接口 |
|---|---|---|
| 28 | 启动并等待 Ready | `POST /devices/{id}/start` → `POST /devices/{id}/wait_ready`（默认 30 秒超时）。仅 Created / Stopped 可点 |
| 29 | 停止 | `POST /devices/{id}/stop` |
| 30 | 删除 | `DELETE /devices/{id}`，两击确认（4 秒后自动解除）。删完进入「墓碑态」 |

#### 送话（目前是三条并行路径 —— 建议合成一条）

| # | 功能 | 接口 |
|---|---|---|
| 31 | 夹具下拉 | `GET /samples`（仓库自带的测试 WAV，如「今天星期几.wav」） |
| 32 | 「选用夹具」 | 把夹具下载回来塞进本地文件框（**多余的一步，应该去掉**） |
| 33 | 本地文件选择 | 接受 `.wav` |
| 34 | 拖拽 WAV | 拖到中栏任意位置即可，只收 `.wav` |
| 35 | 「送出」 | 先 `POST /assets`（带 `device_id`，入库时按设备音频规格转码）→ 再 `POST /devices/{id}/speak {asset_id}` |
| 36 | 音频库下拉 + 「送出所选」 | 直接 `POST /devices/{id}/speak {asset_id}`，格式不符自动 ffmpeg 转码 |
| 37 | 「打断」 | `POST /devices/{id}/interrupt` |
| 38 | 已选音频元信息 | 文件名 · 采样率 · 时长 · asset_id |
| 39 | 送话不可用提示 | 三种文案：Starting 时不能说话 / 连接已结束，重新启动后可再送出 / 启动并 Ready 后可送出 |
| 40 | 快捷键 | `Ctrl`/`⌘` + `Enter` 送出 |

#### 下行播放

| # | 功能 | 接口 |
|---|---|---|
| 41 | 播放器 | `GET /devices/{id}/turns/{turn_id}/audio/downlink`；带标签 + 下载 WAV 链接 |
| 42 | 自动播放 | **只自动播本页面这次会话发起的 turn**；切设备后 WS 从 oldest 回放的历史 turn 一律不自动播 |

#### 配置（现在折叠在「热更新与配置」里）

| # | 功能 | 接口 |
|---|---|---|
| 43 | playingMode 热更新 | `POST /devices/{id}/report {playingMode}`，仅 Ready 时可用，返回 sequence_number |
| 44 | 配置表单 | `GET` / `PUT /devices/{id}/config`，共 24 个字段，分两组锁态： |

**组 A —— 挂靠 / 音频 / 身份（仅 Created、Stopped 可改；Running 改采样率会 409）**

| 字段 | 类型 | 备注 |
|---|---|---|
| `device_id` | 只读 | |
| `environment` / `enterprise` / `device_type` | 下拉，级联 | 改环境要重建下游选项 |
| `server.url` | 只读 | 由环境派生，start 时重解析 |
| `firmware_version` / `nic_type` / `nic_iccid` | 文本 | |
| `playing_mode` | 数字 1-3 | |
| `audio.sample_rate` | 数字 | amr 仅支持 8000 / 16000 |
| `audio.slice_ms` / `audio.max_payload_size` | 数字 | |
| `audio.format` | 下拉 | pcm / wav / mp3 / amr / aac，压缩格式经 ffmpeg 转码并 `-re` 限速推流 |
| `audio.bitrate_kbps` | 数字 | 0=默认，仅压缩格式有效 |
| `uuid.min` / `uuid.max` | 数字 | |
| `behavior.keepalive_interval_sec` | 数字 | 实测服务端约 60s 踢线，这里必须明显更短 |
| `behavior.first_reply_timeout_sec` | 数字 | |
| `behavior.speak_backlog_depth` | 数字 0-64 | 0=关闭（槽占用直接 409）；>0=排队 |
| `behavior.silence_probe` | 勾选 | timeout 静默终态后发探针 report |
| `behavior.interrupt_on_disconnect` | 勾选 | 事件 WS 断开即打断当前 turn |

**组 B —— 录音（Running 也可改）**

| 字段 | 类型 |
|---|---|
| `recording.enable_frame_log` | 勾选 |
| `recording.save_uplink_audio` | 勾选 |
| `recording.save_downlink_audio` | 勾选 |
| `recording.output_dir` | 文本 |

> 只读展示项（不可编辑但需要看见）：`channels`、`sample_format`。

### 2.6 右栏 · 三个页签

#### 页签一：事件（当前设备的原始协议事件）

数据来自 WebSocket `/ws/events?device_id=&instance_id=[&after_event_seq=]`。省略 `after_event_seq` 即从头回放。

| # | 功能 | 说明 |
|---|---|---|
| 45 | 事件计数 | |
| 46 | 「跟随」开关 | 自动滚到最新；用户往回翻时自动松开 |
| 47 | 「复制」 | 把**当前筛选出的**事件复制成 JSONL 到剪贴板 |
| 48 | 范围 chips | 全部 / 关键（去掉 tts_chunk）/ 音频 / 异常 |
| 49 | 文本过滤 | 匹配 event_type、turn_id、reply_kind、turn_end_reason、reason |
| 50 | 事件行 | 时刻（**含毫秒**）+ event_type + `turn_id · reply_kind · turn_end_reason · reason` |
| 51 | 上传/下发聚合行 | 连续的音频包折叠成一条「上传 17 包 · 52 KB」，可展开逐包看序号 + 字节数（来自 `GET /devices/{id}/turns/{turn_id}/frames`，JSONL） |
| 52 | 进行中标记 | 正在推的包组显示「进行中」，每 400ms 轮询帧日志 |

事件类型（决定颜色/图标语义）：`connected` `registering` `registered` `reporting` `ready` `report_echo` `speak_permit` `asr_result` `tts_chunk` `tts_done` `turn_terminal` `interrupted` `speak_queued` `speak_dequeued` `speak_backlog_dropped` `backpressure` `connection_stopped` `connection_failed` `protocol_error` `device_deleted`

#### 页签二：Turn

| # | 功能 | 接口 |
|---|---|---|
| 53 | Turn 列表 | `GET /devices/{id}/turns?instance_id=`；每行：`turn_id`、`reply_kind`、`turn_end_reason` / `uplink_end_reason` |
| 54 | 播放下行 / 下载 | 仅 `reply_kind` 含 tts 时出现 |
| 55 | 当前活动 turn 高亮 | |

#### 页签三：全局

| # | 功能 | 接口 |
|---|---|---|
| 56 | 全局事件总线 | WebSocket `/ws/events/global[?after_global_seq=]`，**跨所有设备**汇聚，`global_seq` 严格递增 |
| 57 | 事件行 | 比设备事件多一个 `device_id` 和 `global_seq` |
| 58 | 懒连接 | 切到本页签才连；断线后下次用 `after_global_seq` 续传；前端最多留 500 条 |

### 2.7 全局行为

| # | 功能 | 说明 |
|---|---|---|
| 59 | 提示条 | info / ok / err 三级；ok 与 info 6 秒自动消失，err 留着不走；`Esc` 关闭 |
| 60 | URL 深链 | `#{device_id}` ，墓碑态带 `?ins={instance_id}`；地址栏可直接分享/刷新恢复 |
| 61 | 墓碑态 | 设备被删或实例已换代后，用同一 `instance_id` 只读回看历史事件与 turn；TTL 24 小时 |
| 62 | 后台节流 | 页面切到后台停轮询，切回立刻补拉一次 |

---

## 3. 状态机（决定按钮禁用与颜色）

**instance_state**（设备实例）

```
created ──start──> starting ──ready──> running ──stop──> stopped
                       │                   │                │
                       └──── failed ───────┴────────────────┴──> (deleted / 墓碑)
```

**connection_state**（这条 WS 连接）

```
disconnected → connected → registering → registered → reporting → ready
```

**按钮可用条件**

| 按钮 | 条件 |
|---|---|
| 启动 | `instance_state ∈ {created, stopped}` |
| 停止 / 删除 | 非墓碑态 |
| 送出 | `instance_state == running` 且（无槽占用 或 backlog 已开启） |
| 打断 | `instance_state == running` |
| playingMode 热更新 | `connection_state == ready` |
| 保存配置（组 A） | `instance_state ∈ {created, stopped}` |
| 保存配置（组 B 录音） | 任何时候 |

**一个 Turn 的生命周期**（时间线要能表达）

```
speak 受理 → [speak_queued 排队]→ speak_dequeued 上槽
   → 上行推包（N 包 / X KB）
   → asr_result（服务端识别结果）
   → tts_chunk × N（下行音频包）→ tts_done
   → turn_terminal（reply_kind + turn_end_reason + uplink_end_reason）
```

---

## 4. 后端已经有、但页面上完全没有的能力

设计时可以考虑要不要放进来（都是现成接口，不用改后端）：

| 能力 | 接口 | 备注 |
|---|---|---|
| **批量启停删** | `POST /devices/batch/{start,stop,delete}` | 这个模拟器的卖点之一就是「批量并发」，但页面只能一台一台点 |
| **播放/下载上行音频** | `GET /devices/{id}/turns/{turn_id}/audio/uplink` | 现在只能听下行，听不到自己送上去的是什么 |
| **同步送话** | `POST /devices/{id}/speak_and_wait` | 送出并等终态，一次调用出结果 |
| **注入故障** | `POST /devices/{id}/faults` | |
| **批量等待条件** | `POST /wait` | |
| **场景编排** | `POST /scenarios/run`、`GET /scenarios/runs/{id}` | 脚本化跑一串动作 |
| **改名/删除配置树节点** | `PUT` / `DELETE /registry/environments/...` | 现在只能加，不能改不能删 |
| **模板管理** | `POST` / `DELETE /templates/{id}` | 现在只能选，不能建不能删 |
| **单个 turn 详情** | `GET /devices/{id}/turns/{turn_id}` | |
| **事件 HTTP 拉取** | `GET /devices/{id}/events` | 现在全走 WS |

---

## 5. 实测出来的具体问题（设计时要解决的）

在 1440×900 视口下实测：

1. **中栏名不副实。** 叫「对话」，但完成一轮完整对话后，中栏显示的仍然只有：状态行 + 3 个按钮 + 3 行送出控件 + 文件框 + 播放器。**约 70% 是空白**。真正的对话内容只在右栏一行小字：`turn_1788418013684207200 / tts · idle / stage2`。

2. **新建面板挤爆侧栏。** 「新建」和名册在同一个滚动列里。展开后：
   - 内容 **1350px 高 × 451px 宽**，被塞进 **361px 高 × 254px 宽** 的滚动框 —— **横竖都要滚**；
   - 「创建」按钮在 y=999px，视口只有 900px，**默认看不见**；
   - 27 个控件、**5 个提交按钮**（添加环境 / 添加厂商 / 添加类型 / 创建 / 按模板创建），没有主次；
   - 名册被压到只剩一行半，设备名被从中间切断。

3. **名册会自己重排。** 按 `last_activity` 排序 + 2 秒轮询，鼠标底下的设备会跳走，容易点错。

4. **三条并行送出路径**，其中夹具还要「选用夹具」再「送出」两步，页面上同时有 3 个像"送"的按钮。

5. **右栏 180px 宽里塞了**：3 个页签 + 4 个 chip + 1 个搜索框 + 2 个工具按钮 —— 全是控件，看不见内容。

6. **配置是 20 个字段的一张平表**，藏在两层折叠里（先「热更新与配置」，再往下翻）。

7. **术语零解释**：`nic_iccid`、`playing_mode`、`简称（wire 值）`、`{enterprise} 占位符`、`stage2`、`conn_generation` 直接裸露在界面上。

---

## 6. 实现约束（设计必须落得下去）

- **无构建工具**：整个前端就三个文件 `index.html` + `app.css` + `app.js`，用 Go `embed` 打进二进制。**不能引入框架、打包器、npm 依赖。** 原生 HTML/CSS/JS。
- **离线机房**：可能取不到 Google Fonts，字体必须能优雅退到系统字体（`Noto Sans SC` / `PingFang SC` / `Microsoft YaHei UI`）。
- **深浅色都要**：机房灯光通常是暗的，深色是常用态，不是附赠。三态：跟随系统 / 浅色 / 深色。
- **高频刷新**：`tts_chunk` 一秒能来几十条，事件区必须能增量渲染，不能整块重画（会把用户正在填的表单冲掉）。
- **中文界面**，等宽字体用于所有 ID、时刻、字节数、协议字段。
- **单页**，无路由库，靠 URL hash 深链。
- 目标视口：桌面 1280–1920 为主，窄到 ~1000px 要还能用。

---

## 7. 给设计的一句话建议

主流程是 **选设备 → 启动 → 送话 → 看回复**，让这条线占满视觉主轴；
配置树、音频库、原始事件、批量操作都是支撑，退到抽屉、弹窗或第二层；
中栏应该真的长得像一段对话（我送了什么 → 设备回了什么 → 这轮怎么结束的），而不是一张控制面板。
