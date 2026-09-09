# Phase 9 提供给 agent 的工具

## 0. 为什么（兼 ADR：CLI，不是 MCP）

REST 端点的粒度不是 agent 任务的粒度。铁证：`POST /devices/{id}/speak_and_wait`
只回 `turn_end_reason` / `uplink_end_reason` / `reply_kind` / `event_seq`
（见 `api/speak.go`），下行实际格式在 `turn.json` 里。agent 想知道「服务端回了什么
格式」最少要打两个端点。

所以本阶段交付的是 **skills + CLI**（`cmd/simctl`），不是 MCP：

- 动词按任务切（送话、下钻一轮、落音频），内部去拼端点。
- `--help` 自描述参数，skill 只写何时用、有哪些坑，两套不会漂。
- 进程就在本机、跟 manager 同仓，不必再挂一个 MCP server。

`cmd/speak` 是 Phase 1 的单机 CLI，不走 manager，不动它。

服务的场景是**交互式排障 + 自主探索**。CI 回归走现成的 `POST /scenarios/run`。
「测试集」是人将来要加的上层概念，现在不猜它的接口。

## 1. 边界（本阶段不做）

- 建 / 删 / 改设备（人在 web 上干）
- `faults` 故障注入
- `interrupt` 打断
- scenario 编排
- TTS 文本合成（只引用音频库现成资产）

人和 agent 共用同一个 manager。这是 `overridden` 那条坑的根，见 §3。

## 2. 九个动词

| 动词 | 干什么 | 底下打谁 |
|---|---|---|
| `up` | 后台起 manager，幂等，**不自动关** | 起进程 + `GET /devices` 探活 |
| `down` | 停 manager | 读 pid 文件 |
| `status` | manager 活着吗 + 几台设备 | `GET /devices` |
| `devices` | 列可选设备，带格式、状态、`overridden` | `GET /devices` 客户端过滤 |
| `assets` | 列音频素材 | `GET /assets` |
| `run` | 选设备 → 送话 → 读判语（核心） | `speak_and_wait` + `GET /turns` |
| `turn` | 某一轮的细节：事件流、帧统计 | `GET /events` + `/frames` |
| `audio` | 把上行/下行音频落到文件 | `GET /audio/{uplink,downlink}` |
| `history` | 列这台设备的每一次运行（含跨重启） | `GET /instances` |

一律 JSON 到 stdout，**不做 `--json` 双形态**。默认是**摘要 + 指针**：给足判断用的
字段，加上 `turn_id` / `instance_id` 供下钻。事件流与帧日志不塞进 `run` 的输出，
要细节走 `turn`。

## 3. `run`

**选设备**

- 位置参数 = `device_id`：`simctl run sim_1 --asset ast_xxx`
- flag 筛选：`--env` / `--enterprise` / `--device-type`。后两个是**简称**不是名称
  （`CONTEXT.md` 警告过这个混淆）。`--env` 对的是环境名（环境没有简称）。
  `GET /devices` 每行已带这些字段，客户端过滤，**不改服务端加过滤参数**。
- 选中 **0 台 → 报错退出**，绝不返回空数组。打错简称是选中 0 台最常见的原因，
  静默返回空数组会被 agent 读成「这个厂商的设备全都不回话」。
- ~~选中 N 台 → **全跑**~~ **Phase 10 改了这条**：跑几台由过滤粒度决定——给到
  `--device-type` 就是该类型下随机一台，停在 `--env` / `--enterprise` 才全跑。
  跑之前还会先占一道租约。见 `phase10.md` §2 §3。输出仍是数组。

**配置层：默认用落盘定义（不是内存里的当前值）**

- `Created` / `Stopped` 且 `overridden` → **自动 `POST /config/reset` 再 start**，无声
- `Running` 且 `overridden` → **报错**「这台在跑且当前值≠定义，我不动它」，`--dirty` 放行
- 理由：`POST /config/reset` 在 Running 时返回 409（phase7.md §3），要「用定义」
  就得先 stop，而那台可能正是人在 web 上看着的。人和 agent 共用同一个 manager，不能抢。
- 设备没启动就自动 `start` + `wait_ready`

**素材**：只引用音频库现成资产（`--asset <id>`）。不做 TTS 合成。

**实现**：内部合并 `POST /devices/{id}/speak_and_wait` 与 `GET /devices/{id}/turns`，
因为 `down_format` 不在前者的响应里。`GET /turns` 为此透出 `down_format` /
`up_format` / `down_bytes`（前两个 turn.json 里早就有，查询端点原先没带上）。

`run` 的摘要必须含：`device_id`、`instance_id`、`turn_id`、`verdict`、
`turn_end_reason`、`uplink_end_reason`、`reply_kind`、`up_format`、`down_format`、
`down_bytes`、`overridden`。

## 4. 判语（`verdict`）

只返事实，不内置断言（不做 `--expect-xxx`，不出 pass/fail）。`verdict` 是协议事实的
分类，不是业务判断：`no_reply` 就是没回话，「没回话是不是 bug」仍归 agent。

对照 phase2.md §6.7 终止表，必须是全函数（11 行每行都有归宿）。
`reply_kind` 取值：`tts` / `command` / `json` / `command+tts` / `json+tts` / `silent` / 空。

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

查表规则：

| verdict | 条件 |
|---|---|
| `replied` | `turn_end_reason=idle` 且 `reply_kind` 含 tts 且 `down_bytes > 0` |
| `replied_no_audio` | `idle` 且 `reply_kind` 为 `command` 或 `json`（无 tts） |
| `silent` | `idle` 且 `reply_kind=silent`，**或**含 tts 但 `down_bytes == 0` |
| `no_reply` | `turn_end_reason=timeout` |
| `error` | `turn_end_reason=error` |
| `aborted` | `turn_end_reason=interrupt` 或 `connection_lost` |

含 tts 但 `down_bytes == 0` 的老录音回落见 §5：`down_format` 非空就当 `replied`，
不能因为老数据一律判 `silent`。

## 5. 契约变更：`turn.json` 增 `down_bytes`

本阶段**唯一**新记的字段。`recording.TurnRow` 加 `DownBytes`，由 recorder 累加每个
下行 TTS 帧的 `payload_len`。`GET /devices/{id}/turns` 与
`GET /devices/{id}/turns/{turn_id}` 透出。

Phase 9 之前的老录音没有这个字段 → 读回来是 0。此时 `verdict` 回落：`down_format`
非空就当 `replied`。

## 6. 进程管理

- `up` 先 `go build -o data/manager.exe ./cmd/manager` 再起（`data/` 已在
  `.gitignore`，产物不入库）。
- simctl 接 `--config`（默认 `configs/manager.yaml`）和 `--listen`（默认
  `127.0.0.1:8090`）。`cmd/manager` 的监听地址是 `--listen` flag，**不在
  `manager.yaml` 里**；manager 自己的 `--config` 没有默认值，必须显式给。
- 探活：`GET /devices` 通就算活，**不新加 `/healthz`**。
- pid 写 `data/manager.pid`，日志追加到 `data/manager.log`（manager 只往 stderr 打，
  不重定向的话 agent 起完什么也看不见）。
- **不自动关**。只有人或 agent 显式 `down` 才停。
- 平台是 Windows，进程要能在 simctl 退出后继续活着。

## 7. skill

放本仓库 `.claude/skills/simctl/`（跟着 repo 走）：

- `SKILL.md` — 薄。这个 CLI 是干什么的、`up` 怎么起、下面两份文件各管什么。
- `references/run.md` — 选设备 → 送话 → 读判语。含终止表、verdict 六值、`overridden` 那条坑。
- `references/inspect.md` — 事件、帧、音频、历史运行。

CLI `--help` 自描述动词与参数，skill 不抄动词列表。skill 只写何时用、有哪些坑。
