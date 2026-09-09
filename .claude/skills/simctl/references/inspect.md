# inspect：事件、帧、音频、历史

查某一轮或某一次运行时用。参数见 `simctl --help`。`turn` / `audio` 要同时使用目标轮的 `instance_id` 和 `turn_id`；`history` 只需 `device_id`。设备跨启停长期存在，Manager 重启才换新实例，stop/start 只换连接代次。沿用结果返回的 ID，不拿当前实例 ID 代替历史实例 ID。

## turn

某一轮的事件流 + 帧统计（不是把 frames.ndjson 整份倒出来）。

- 事件按 `turn_id` 过滤；同 instance 上其它轮不会混进来。
- 帧日志方向是 `outbound`（设备→服务端，上行）和 `inbound`（服务端→设备，下行），不是 up/down 这两个词。
- **帧日志异步落盘**：`run` 刚返回就查 `turn`，帧统计可能少最后一两条。轮的终态字段（`verdict` / `turn_end_reason`）在 `run` 的返回里就是准的，别拿帧数去判断这一轮完没完。
- `frames.inbound_bytes` 含非音频的下行帧，`turn.down_bytes` 只算 TTS payload。两者不等是正常的。
- `source` 为 `live` / `tomb` / `disk`：盘上历史没有 TTL，墓碑有。别把「manager 重启后还能查」理解成墓碑。

## audio

把这一轮的上行或下行音频落到文件。stdout 仍是 JSON 指针（路径、字节数），二进制不进 stdout。

- 默认走回放端点（pcm 会包成 wav）。下行 AMR 会剥重复存储头，和 UI 试听同一条路。
- 上行格式记在 turn.json 的 `up_format` 里；跨重启不要去问设备「现在」配的是什么。

## history

`GET /devices/{id}/instances`：这台设备每一次运行，含当前、墓碑、盘上历史。manager 重启后旧 `instance_id` 还在这个列表里，拿它去 `turn` / `audio`。

当前运行还没送过话时盘上可能没有目录，列表里仍应有这一项（`source=live`）。删历史是人的事，simctl 不提供删。
