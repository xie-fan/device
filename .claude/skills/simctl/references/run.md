# run：选设备 → 送话 → 读判语

`simctl run` 是核心动词。内部拼 `POST /devices/{id}/speak_and_wait` 和 `GET /turns/{turn_id}`——下行实际格式和 `down_bytes` 只在后者里。参数见 `--help`。

## 选设备

- 位置参数是 `device_id`；`--env` 对**环境名**；`--enterprise` / `--device-type` 对**简称**，不是名称（配置树里两套名字，上线报文用的是简称）。
- 过滤在客户端做，不改服务端。
- **选中 0 台是错误**，不会给空数组。打错简称是最常见原因；空数组会被读成「这个厂商的设备全都不回话」。
- **不传设备 ID 和过滤条件会选中全部设备**。默认显式指定设备 ID；批量任务先用 `devices` 配合相同过滤条件核对范围，只运行用户要求的目标。
- 选中 N 台全跑，默认串行，输出始终是数组。

## `overridden`（人和 agent 共用 manager）

落盘的是「设备定义」，内存里的是「本次运行的当前值」。人在 web 上改当前值不会写回定义，`overridden=true`。

- `Created` / `Stopped` 且 overridden → run **自动** `POST /config/reset` 再 start，CLI 不会另行确认。执行前说明会丢弃临时覆盖；用户是否要保留当前值不明确时先询问。
- `Running` / `Starting` 且 overridden → **报错**「这台在跑且当前值≠定义，我不动它」。用户明确要求使用当前覆盖配置时才加 `--dirty`；它不是通用重试开关，也不能阻止停止设备的自动 reset。
- 原因：Running 时 reset 返回 409；要回到定义就得先 stop，而那台可能正是人在看着的。不要抢。

设备没启动就 start + `wait_ready`。素材只引用音频库现成资产，不做 TTS 合成。

## 调用失败与重试

- 数组元素带 `error` 表示该设备调用失败；同批其它设备可能已经完成，先逐项检查再决定重试范围。
- 启动失败先看错误中的 `last_error`；信息不足时查 `data/manager.log`。
- 超时或连接中断不保证送话没有发生。先查该设备的轮次/历史，确认是否已生成结果，再决定是否重新送话，避免重复执行。
- CLI 不提供设备创建、素材导入或故障注入；用户要求这些操作时另行使用相应 UI/API 工作流，不把它们猜成 simctl 动词。

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

Phase 9 之前的老录音没有 `down_bytes`（读出来是 0）。含 tts 且 `down_format` 非空则当 `replied`，不能因为老数据一律判 `silent`。`down_format` 为空表示这一轮没有下行音频。

`run` 只给摘要 + `turn_id` / `instance_id`。事件流和帧日志走 `turn`。
