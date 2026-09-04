# Phase 8 跨重启的历史

## 0. 为什么

Phase 7 把设备定义落了盘，设备跨 manager 重启存活了——但它的**历史没跟着活下来**。
`instance_id` 是每进程新生成的（`POST /devices` 时分配一次，stop/start 只换 `conn_generation`），
而录音与事件按 instance 分区（`recordings/{device_id}/{instance_id}/{turn_id}/`，查询必填 `instance_id`）。
重启后同一台设备换一个新 instance，**旧目录还躺在盘上，API 却一律 404 `instance 未命中`**。

这是 phase7.md §5 里明写的已知取舍。那里否掉了两条修法，都是因为要动已经稳定的 instance 世系：

- 启动时扫盘把旧 instance 注册成 tombstone → 得重新定义 tombstone TTL 语义（现在是删设备后才进墓碑）；
- 让设备沿用 `last_instance_id` → 两次运行的事件游标会串在一起。

本阶段走第三条：**世系一个字不动，改的是「一个 instance 从哪里读」**。

## 1. 盘就是历史的真相源

instance 的解析多认一种来源。`instance_id` 仍不复用，两次运行的事件游标仍互不相通，
tombstone 的 TTL 语义也没动——只是盘上有目录就能读。

| 来源 | 认它的条件 | turn 元数据 | 事件 |
|---|---|---|---|
| `live` | 内存里的设备，且 `instance_id` 对得上 | 内存 | 内存 ring buffer |
| `tomb` | 墓碑未过期（删设备后 TTL 内） | 内存 | 内存 ring buffer |
| `disk` | `recordings/{device_id}/{instance_id}/` 存在 | 各 `turn.json` 的最后一行 | `events.jsonl` |

`instView` 把三种来源收成同一份只读视图，五个历史端点（`/turns`、`/turns/{id}`、`/frames`、
`/audio/{uplink,downlink}`、`/events`）不再各写一遍三分支。视图里的 turn 是**锁内拷贝的值**
而不是指针：读盘必须在 `s.mu` 之外做，而 live 设备会继续改自己那份记录。

`GET /turns` 的响应加 `source` 字段说明这一份是从哪来的。

## 2. 事件必须落盘

turn 元数据、帧日志、音频本来就在盘上，**只有事件日志是纯内存的 ring buffer**。
不落盘的话「历史」只有半份：能看见有过哪几轮、听得到音频，却说不出这一轮里
服务端回过什么。而事件正是 agent 断言的那一面。

`recordings/{device_id}/{instance_id}/events.jsonl`，一行一个事件，
形状与 `GET /events` 的返回**逐字节相同**（同一个 `MarshalJSON`），读回来不必再转一次。

写入走 `EventLog.SetMirror` 挂到 `recording.Recorder` 的异步队列上。这条约束是硬的：
**mirror 在 `EventLog` 的临界区内被调，禁止同步 IO，只许拿叶子锁**。`Recorder.submit`
拿的是自己的锁且发送不阻塞（队列满即丢），符合。`Server.Close` 负责在实例 Shutdown
**之后**排干——收尾事件还要写进去。

盘上是全量追加，没有 ring buffer 的淘汰，所以 `disk` 来源的 `GET /events`
**不存在游标过期**，`after_event_seq` 之后的一律给出，不会 410。

## 3. 上行格式自描述

`turn.json` 补 `up_format` / `up_sample_rate` / `up_channels`。

上行回放原本靠「设备**现在**的配置」定格式（`live.cfg.Audio`）。跨重启回看时那份配置
未必还是录这段时的那一套——设备可能已经改过格式，甚至已经删了。现在按 turn 记，回放优先采信它；
Phase 8 之前的老录音没有这几个字段，回落到设备当前配置，即原来的行为。

（下行早在 Phase 5e 就有 `down_format` / `down_sample_rate` 了，这次是把上行补齐。）

## 4. 新增与变更的端点

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/devices/{id}/instances` | 列出这台设备的每一次运行：`instance_id`、`source`、`turns`、`started_at`/`ended_at` |
| `DELETE` | `/devices/{id}/instances/{instance_id}` | 删掉一次运行的录音与事件；**本次运行返回 409** |
| `GET` | `/devices/{id}/turns` | 响应增 `source`（`live`/`tomb`/`disk`） |
| `GET` | `/devices/{id}/{turns,frames,audio,events}` | 语义变更：旧 instance 不再 404，从盘上读 |

`GET /instances` 是**发现入口**——没有它，重启后谁也说不出旧 `instance_id` 叫什么，
历史等于取不到。本次运行即使还没送过话（盘上没有目录）也必须出现在列表里。

时间范围只读首尾两个 `turn.json`：`turn_id` 是 `turn_<UnixNano>`，位数相同，
字典序即时间序，不必把整个 instance 翻一遍。

**`DELETE /instances/{id}` 是新长出来的必要品**：tombstone 到期本来会自动清盘上目录，
但重启后 tombstone 就没了，那些目录会永远留着。历史现在看得见，就得有地方能删。

## 5. `/wait` 与 `/ws/events` 仍只认内存

这两个等的是**将来的事件**，盘上历史没有将来，对它们 404 才是对的。
它们走 `resolveLiveLocked`（就是原来那个解析，只认 live / tombstone），
不走新的 `resolveInstance`。

## 6. 界面

设备操作区多一个「历史运行」抽屉，列出每一次运行，点回看进只读视图，点删除清掉那一次。

- 盘上的旧运行没有 live WS，事件改用 `GET /events` **一次性补齐**，仍走 `ingestEvent`
  这条通道——对话轴、帧统计、播放按钮都是现成的，没有第二套渲染。
- **只读态分来源**：墓碑是「设备已删、TTL 内可看」；盘上历史是「设备还在，只是那次运行结束了」。
  横幅文案、状态位（`archived` 而非 `deleted`）、送话不可用的理由，三处都跟着分——
  对着一台活得好好的设备说「deleted」是误导。
- 来源以 `GET /turns` 响应里的 `source` 为准，不靠调用方传：URL 直接带 `?ins=` 进来
  （分享一条历史链接）也不会说错。

## 边界（本阶段不做）

- **事件文件的轮转与上限**。一次运行的 `events.jsonl` 全量追加，长跑的设备会一直涨。
  内存那份仍受 `event_log_max_entries` 约束，只有盘上这份没有。
- **`instance_id` 之外的检索**。仍是「按设备列运行 → 按运行看轮次」两级，
  没有跨设备的时间线，也不能按事件类型全局搜。
- **盘上历史的 `GET /devices/{id}/config`**：旧运行用的是当时的配置，没有记下来。
  回看时看到的配置是设备**当前**的定义。轮次自己的音频参数由 §3 保证准确。
- **sqlite**。跟 Phase 7 一样：整份重写的 YAML + 目录树够用，代码里留了 `ponytail:` 标记。
