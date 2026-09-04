# run：选设备 → 送话 → 读判语

`simctl run` 是核心动词。内部拼 `POST /devices/{id}/speak_and_wait` 和 `GET /turns/{turn_id}`——下行实际格式和 `down_bytes` 只在后者里。参数见 `--help`。

## 选设备

- 位置参数是 `device_id`；`--env` 对**环境名**；`--enterprise` / `--device-type` 对**简称**，不是名称（配置树里两套名字，上线报文用的是简称）。
- 过滤在客户端做，不改服务端。
- **选中 0 台是错误**，不会给空数组。打错简称是最常见原因；空数组会被读成「这个厂商的设备全都不回话」。
- 选中 N 台全跑，输出始终是数组。

## `overridden`（人和 agent 共用 manager）

落盘的是「设备定义」，内存里的是「本次运行的当前值」。人在 web 上改当前值不会写回定义，`overridden=true`。

- `Created` / `Stopped` 且 overridden → run **自动** `POST /config/reset` 再 start，无声。要用定义，不要用桌上那份临时值。
- `Running` 且 overridden → **报错**「这台在跑且当前值≠定义，我不动它」。`--dirty` 才放行。
- 原因：Running 时 reset 返回 409；要回到定义就得先 stop，而那台可能正是人在看着的。不要抢。

设备没启动就 start + `wait_ready`。素材只引用音频库现成资产，不做 TTS 合成。

## 判语

`verdict` 只分类协议事实，不是 pass/fail。`no_reply` 就是没回话；是不是 bug 仍归你。

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

Phase 9 之前的老录音没有 `down_bytes`（读出来是 0）。含 tts 且 `down_format` 非空则当 `replied`，不能因为老数据一律判 `silent`。`down_format` 为空表示这一轮没有下行音频。

`run` 只给摘要 + `turn_id` / `instance_id`。事件流和帧日志走 `turn`。
