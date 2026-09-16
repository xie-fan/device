# run：选设备 → 送话 → 读判语

`simctl run` 是核心动词。内部拼 `POST /devices/{id}/speak_and_wait` 和 `GET /turns/{turn_id}`——下行实际格式和 `down_bytes` 只在后者里。参数见 `--help`。

## 选设备

- 位置参数是 `device_id`；`--env` 对**环境名**；`--enterprise` / `--device-type` 对**简称**，不是名称（配置树里两套名字，上线报文用的是简称）。
- 挑设备在客户端做，不改服务端。
- **挑不到设备是错误**，不会给空数组：设备册为空、或点名的 device_id 不存在，都报错退出。
- 输出始终是数组，每台一个元素。

**三级是挂靠，不是筛选**（Phase 11）。设备册条目只有 `device_id`，`run` 时才把设备挂上去，所以 `--env` / `--enterprise` / `--device-type` **三个必须给全**，缺一个直接报错。

| 给的条件 | 跑几台 |
|---|---|
| `device_id` + 三级 | 就那一台，挂成三级说的样子 |
| 只给三级 | 从**整个设备册**随机挑一台 |
| 三级 + `--count N` | 随机挑 N 台 |
| 三级 + `--count 0` | 整册全跑（默认串行，`--parallel` 才并行） |

用户说「测一下 X 机型」时给三级就够，不必先 `devices` 挑 id——设备册里哪一台都能挂成 X。**整册全跑是批量任务**：先用 `devices` 核对范围，只运行用户要求的目标。

`devices` 动词的同名过滤仍是筛选，筛的是**当前挂靠**，只对跑着的设备有意义。

## 产品与临时覆盖（Phase 12）

设备本身没有属性：音频格式、对话模式、身份字段、超时、拍照开关都来自**产品**，start 时合成。

- `--product <id>` 选产品。不给就用设备类型的默认产品；类型没配默认产品时报错。可选产品用 `products` 查。运行中的 manager 没有产品端点时（`products` 回 404），不带 `--product` 照样能跑，结果里 `product` 为空。
- `--set 路径=值` 临时覆盖一个字段，可重复。路径与 `GET /devices/{id}/config` 的字段名一致：`audio.format=mp3`、`playing_mode=3`、`behavior.first_reply_timeout_sec=40`、`features.photo.enabled=true`。
- 值按 JSON 解析：数字与 `true` / `false` 原样发出；字符串要带引号，例如 `nic_iccid="89860325..."`。不带引号的纯数字会按数字发出，服务端返回 400。
- 覆盖不受产品的支持清单限制，清单外的格式照样跑；与产品默认值相同的字段不算覆盖。
- start 换了产品，覆盖全部清空；同一产品 stop / start 保留。

**身份字段属于产品。** 换一个 ICCID 不同的产品，或 `--set nic_iccid=...`，真实服务端会重新校验这台设备；校验不过会把它标成不可用，记录留在共享环境里。对真实环境跑之前，确认产品里的 ICCID、网卡就是这台设备在服务端登记的值；用户没要求时保持身份字段不变。

**这些参数只在 start 那一刻生效。** 设备已在跑时，`run` 复用现有连接，挂靠三级、`--product`、`--set` 都不起作用，也不报错。结果里的 `product` / `overrides` 是设备实际值，拿它核对；三级用 `devices` 核对。要换就先 `POST /devices/{id}/stop` 再 `run`。

## 租约（并发不撞车）

跑之前先 `POST /devices/{id}/lease` 占住，跑完还，所以两个并发 run 不会撞同一台。

- 随机档撞上被占的会自动换下一台；**跑失败不换台**，故障照报。全被占则报错退出。
- 批量档里被占的那台是一个带 `error` 的数组元素，不是被跳过——数组和选中集一一对应。
- 点名某台而它被占：直接报错，不换台。
- 租约**不挡人在调试台上的操作**，只在 run 之间生效。TTL 3 分钟，进程重启即失效。
- `--force` 抢占别人的租约。**确认那个 run 已经死了再用**（Ctrl-C 杀掉的、跨机器的僵尸租约），它不是通用重试开关。正常撞车应该换台或等，不是抢。

## `overridden`（人和 agent 共用 manager）

`overridden=true` 表示设备上有临时覆盖：人在调试台上改的，或上一次带 `--set` 的 run 留下的。覆盖只活在 manager 内存里，重启即清空。

- `Created` / `Stopped` 且 overridden → run **自动** `POST /config/reset` 再 start（带本次的 `--product` / `--set`），CLI 不会另行确认。执行前说明会丢弃临时覆盖；用户是否要保留当前值不明确时先询问。
- `Running` / `Starting` 且 overridden：覆盖与本次 `--set` 一致 → 复用；不一致 → **报错**「我不动它」。用户明确要求使用当前覆盖配置时才加 `--dirty`；它不是通用重试开关，也不能阻止停止设备的自动 reset。
- 原因：Running 时 reset 返回 409；要回到产品默认值就得先 stop，而那台可能正是人在看着的。不要抢。

设备没启动就 start + `wait_ready`。素材只引用素材库里现成的音频资产，不做 TTS 合成。

## 调用失败与重试

- 数组元素带 `error` 表示该设备调用失败；同批其它设备可能已经完成，先逐项检查再决定重试范围。
- 启动失败先看错误中的 `last_error`；信息不足时查 `data/manager.log`。
- 超时或连接中断不保证送话没有发生。先查该设备的轮次/历史，确认是否已生成结果，再决定是否重新送话，避免重复执行。
- CLI 不提供设备创建、产品管理、素材导入或故障注入；用户要求这些操作时另行使用相应 UI/API 工作流，不把它们猜成 simctl 动词。

## 判语

`verdict` 只分类协议事实，不是 pass/fail。`no_reply` 就是没回话；是不是 bug 要根据用户的预期判断。没有给出业务预期时，只报告事实，不擅自宣布测试通过。

对照终止表（11 行都有归宿）：

| 路径 | reply_kind | turn_end_reason | verdict |
|------|------------|-----------------|---------|
| 仅 TTS idle | tts | idle | `replied`（`down_bytes > 0`） |
| 仅 command | command | idle | `replied_no_audio` |
| 仅成功 JSON | json | idle | `replied_no_audio` |
| command+TTS | command+tts | idle | `replied`（`down_bytes > 0`） |
| JSON+TTS | json+tts | idle | `replied`（`down_bytes > 0`） |
| 仅 IsFinal 无终态 | silent | idle | `silent` |
| 仅 interim 或全无（正常） | 空 | timeout | `no_reply` |
| 同上且 fault 为 drop 行 | 空 | timeout | `no_reply` |
| 失败 JSON | 空 | error | `error` |
| interrupt | 保持或空 | interrupt | `aborted` |
| 连接收口 Phase C | 保持或空 | connection_lost | `aborted` |

查表：

| verdict | 条件 |
|---|---|
| `replied` | `idle` 且 `reply_kind` 含 tts 且 `down_bytes > 0` |
| `replied_no_audio` | `idle` 且 `reply_kind` 为 `command` 或 `json` |
| `silent` | `idle` 且 `reply_kind=silent`，**或**含 tts 但 `down_bytes == 0` |
| `no_reply` | `turn_end_reason=timeout` |
| `error` | `turn_end_reason=error` |
| `aborted` | `interrupt` 或 `connection_lost` |

**`replied` 不等于有回答。** 配了 `hint.path` 的机型，服务端在 VAD（下行 `Stage=4`）后几十毫秒先发一包提示音，`A3.amr` 约 1900 字节、0.62 秒。之后再没有 TTS 分片时，判语仍是 `replied`，其实没回答。判断有没有回答：`down_bytes` 明显大于提示音，或 `turn` 的事件里 `vad` 之后还有持续的 `tts_chunk`。

**上行素材太短，整轮可能没反应。** 不到 2 秒、结尾没有静音的素材，服务端可能拿不到 ASR final，连提示音都不发，判语 `no_reply`（不同区域表现不一）。素材做到 2.5 秒以上，或用 `ffmpeg -af "adelay=600,apad=pad_dur=1.2"` 补前后静音再导入。

**拍照。** 服务端在这一轮下发拍照指令时，结果的 `photo.command=true`。设备按产品的拍照功能传图（`photo.uploaded=true`），之后等到的图片分析语音算本轮回复：`reply_kind=command+tts`，判语 `replied`。等回复超时则 `reply_kind=command`，判语 `replied_no_audio`；此时看 `photo.skipped`（没开拍照 / 没配图 / 读图失败）区分「没传图」和「传了图没回答」。服务端不回传图 ack，传成功与否只能看后续回复。

Phase 9 之前的老录音没有 `down_bytes`（读出来是 0）。含 tts 且 `down_format` 非空则当 `replied`，不能因为老数据一律判 `silent`。`down_format` 为空表示这一轮没有下行音频。

`run` 只给摘要 + `turn_id` / `instance_id`。事件流和帧日志走 `turn`。
