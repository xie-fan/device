# Phase 1 详细设计：单设备协议正确闭环（v8）

Turn 占用、先写不改、fault 矩阵、`pending_reports`、下行计时、ACK 字段见 `architecture.md`。

## 1. 目标

跑通：握手 → register ACK → **初始 report 精确序号回显 → Ready** → pcm 上行 → 接收 TTS。  
注入只按矩阵验收。one-shot CLI。音频与指令均实现 **binary ACK**（SleepMs=0）。

## 2. 范围与非目标

**范围**

- 独立 protocol 包（AudioHeader、管理信封、无前缀 JSON、`'4'` ACK）
- Connection / UplinkTurn / DownlinkPlayer
- Turn（Reserved 时分配 id/uuid；`uplink_end_reason` / `turn_end_reason`）
- `pending_reports[sequence]`；keepalive 走同一计数器
- 首包等待 + 下行 idle 计时（`--wait` 必在超时内结束）
- 正常路径本地校验 + 注入矩阵
- 内部 PCM 切片上线（format=pcm）
- 帧录制与音频落盘
- YAML 配置校验
- `cmd/speak`、`cmd/check`、`cmd/fixture`
- 音频 **与指令** binary ACK（SleepMs=0）

**非目标**

- 批量设备、Web UI、Scenario 引擎、REST Manager
- 连续模式时间轴（Phase 2）
- JSON ACK、非零 SleepMs、节流 A/B/C（Phase 2）
- queue、线上 wav/mp3
- CLI 查询 Mongo 或服务端 `DownlinkAck`
- 运行中修改 `device_id`（本阶段身份在进程启动时确定）

## 3. 目录结构

```text
toy-device-simulator/
├── protocol/
│   ├── header.go
│   ├── message.go
│   └── header_test.go
├── core/
│   ├── device.go
│   ├── turn.go
│   ├── events.go
│   ├── conn_sm.go
│   ├── uplink_sm.go
│   ├── downlink_sm.go
│   └── pending_reports.go
├── config/
├── recording/
├── cmd/
│   ├── speak/
│   ├── check/
│   └── fixture/              # alloc-fresh-id；本阶段必须交付
├── configs/
│   └── example_device.yaml
└── testdata/
    ├── golden_frames/
    ├── fixtures/fresh_ids.jsonl
    ├── inbound/              # 可选：录制的下行指令帧，供指令 ACK 单测/注入
    └── audio/                # 源文件可为 wav，上线为 pcm
```

## 4. protocol 包

- `AudioHeader` 固定 100 字节，小端，含 2 字节 padding。
- 提供 encode / decode；golden 对照对齐基线 `common/types/newProtocol.go` 的 `AudioHeader` 与协议手写样例。
- **禁止**把 `example/asr/mock.go` 发出的帧收进 golden。
- 管理首字节 ASCII `'1'`（0x31）；音频 `'0'`（0x30）；识别 `'4'` 与 `'{'`。
- 必须能编码 binary ACK（音频与指令字段见 `architecture.md` §4.9）。

**mock.go 已知偏差（不要复制）：**

- 管理首字节 `0x01` 而非 `'1'`
- 不发 report
- 主路径不发 Stage=2
- UUID 写死为 1
- 切片间隔注释与代码不一致

## 5. 正交状态机

### 5.1 Connection 与 pending_reports

```text
Disconnected
  → Connecting（握手 Header Device / Action=chatbot）
  → Connected
  → Registering
  → Registered          # '1' + topic 以 /register/client 结尾且 data.code==0
  → Reporting           # 已发出 kind=initial 的 report，等待精确序号回显
  → Ready               # 仅 initial waiter 命中后转入
```

- 不要把 `/report/client` 解析成 `AckResponse`，不要等 `data.code`。
- 匹配规则见 `architecture.md` §4.8：按 `sequence_number` 查表，禁止 latest。
- register `code != 0`（含 5001）→ `ack_failure`（事件带 `correlation_id`）。
- initial report 超时 → `report_timeout`，不是 `ack_failure`；不得进入 Ready。
- 注入 `skip_register`：停在 Connected，不发 register。
- 注入 `skip_report`：Registered 后停住，不发 report，不进入 Reporting。
- keepalive 仅 Ready 之后；每次 keepalive 新增 pending 项，回显不改变 Connection。

本阶段事件：连接级用 `correlation_id`（含 Turn 期间的 keepalive `report_echo`）；CAS Reserved 之后的对话事件用 `turn_id`。

**乱序验收（本阶段必须有自动化测试）：**

- 向 matcher 先后注入回显 seq=2、seq=1（或等价乱序），两个 waiter 各自完成。
- 若 seq=1 为 initial：Ready 只在 seq=1 命中时发生，不得因 seq=2 先到而 Ready。
- 各 `report_echo` 的 `correlation_id` 与发送时绑定，不得交叉。

### 5.2 UplinkTurn

- 正常路径：仅 `Connection == Ready` 后 CAS Reserved。
- 注入：`skip_register` 在 Connected 上 CAS；`skip_report` 在 Registered 上 CAS。

```text
（槽空）--CAS--> Reserved → Speaking → FinishingUpload → WaitingReply → Terminal

Speaking + 收到 Stage=4
  → 若 uplink_end_reason 为空则立即记 vad
  → FinishingUpload（停发 Stage=1，补 Stage=2）
```

进入 WaitingReply 后按 `architecture.md` §4.4 启动 `first_reply_timer`。  
Phase 1 one-shot 无第二次 speak；状态机仍实现 Reserved 与取消表（供单测与 Phase 2）。取消规则见 `architecture.md` §4.7。Reserved 取消：`uplink_end_reason` 保持空。

### 5.3 DownlinkPlayer 与 ACK

```text
Idle → PlayingTTS（匹配 uplink_uuid 的 Stage=1）→ Idle（idle / 新一轮 / 打断）
```

- 音频 `NeedAck == 1` → 发 `'4'`，字段按架构音频 binary 表；SleepMs=0。
- 指令 `need_ack == 1` → 发 `'4'`，字段按架构指令 binary 表；SleepMs=0。
- 标志为 0 → 不发 ACK。
- 不读取服务端 `DownlinkAck`。本阶段不测节流。

若联调会话期间 core 未下发指令：用 `testdata/inbound/` 或接收循环注入一帧合法 `/command/client`（`need_ack=1`）验收出站 ACK；不得把「本趟没收到指令」写成实现免除。

指令通道与三套状态机正交。读循环不因落盘阻塞。

**硬规则：** 正常路径 Seq=0 + Stage=1；Stage=4 先记 vad 再补 Stage=2；下行不依赖 Stage=2。

## 6. Turn

Reserved 时写入 `turn_id`、`uplink_uuid`。  
`uplink_end_reason` 一旦非空（含 `vad`），后续 Stage=2 / 取消不得改写。

## 7. 配置

```yaml
device:
  enterprise: "demo"            # 须已在目标 core 配置
  device_type: "A3"             # 禁止 MH 前缀；须已有设备类型配置
  device_id: "sim_001"          # 进程启动身份；skip_register 用夹具 ID 启动新进程，不运行中改键
  action: "chatbot"
  firmware_version: "1.0.0"
  nic_type: "wifi"
  nic_iccid: "8986xxxxxxxxxx"   # 新设备须能过 iot.Check，或使用已有 status=1 设备
  playing_mode: 1

  audio:
    format: "pcm"               # Phase 1/2 线上唯一合法值
    sample_rate: 16000
    channels: 1
    sample_format: "s16le"
    slice_ms: 100
    max_payload_size: 51200     # 仅正常路径；oversize 注入不受此限

  behavior:
    auto_register: true
    auto_report: true
    keepalive_interval_sec: 60
    keepalive_method: "report"
    report_sequence_start: 1    # 原子计数器初值，此后每次 report +1
    report_echo_timeout_sec: 5
    first_reply_timeout_sec: 20
    downlink_idle_timeout_sec: 20
    expect_downlink_need_ack: false   # 仅 fixture 注释，不是运行时开关
    downlink_ack:
      mode: binary              # Phase 1 锁定 binary
      sleep_ms: 0
      code: 0

  uuid:
    min: 1
    max: 2147483647            # 0x7FFFFFFF

  server:
    url: "ws://127.0.0.1:8089/"

  recording:
    enable_frame_log: true
    save_uplink_audio: true
    save_downlink_audio: true
    output_dir: "./recordings"
```

线上 `format` 不是 `pcm` → `local_validation_error`，不连网。  
`--audio testdata/hello.wav`：解 RIFF 成内部 PCM 再分帧；帧 payload 为 pcm 字节，无 RIFF。  
Phase 1 配置写成 `mode: json` 或 `sleep_ms != 0` → 启动拒绝（本阶段不交付）。

**最小联调前置：** 对齐基线对应的 core 已启动；企业与设备类型已配置；正常路径设备 `status=1` 或新设备 ICCID 能过 IoT。

## 8. CLI 与夹具

```bash
go run ./cmd/check --config configs/example_device.yaml

go run ./cmd/fixture alloc-fresh-id --run-id <uuid> --out testdata/fixtures/fresh_ids.jsonl

go run ./cmd/speak --config configs/example_device.yaml --audio testdata/hello.wav --wait

go run ./cmd/speak --config ... --audio testdata/hello.wav --wait \
  --inject skip_register --device-id "$(夹具分配的 id)"

go run ./cmd/speak --config ... --inject skip_report --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject bad_seq --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject oversize --wait --audio testdata/hello.wav
go run ./cmd/speak --config ... --inject bad_header --wait --audio testdata/hello.wav
```

`--wait`：阻塞到 Turn Terminal。负向 drop 用例由 `first_reply_timeout_sec` 收口，禁止无限等待。

- speak **不**查询 Mongo，**不**判断 `status=1`。
- `--device-id` 仅作为本进程启动身份（与配置文件合并），不是运行中重键。
- 夹具：隔离 core；`id = sim_sr_{run_uuid}_{n}`；本趟 setup 不对该 id register；jsonl 含 `id, run_uuid, isolation, generated_at`。
- `skip_register` 验收必须读 jsonl，不能只看退出码。
- `bad_header` 出站：第一字节 `'0'`，其后恰好 10 字节 `0x00`。
- `bad_seq`：CAS Reserved 后发 Seq>=1，不断言「本地无 active」。

不提供独立多进程 `start` / `interrupt` / `status`。

## 9. 帧录制

```text
recordings/{device_id}/{turn_id}/
  ├── frames.jsonl
  ├── uplink.pcm
  ├── downlink.<ext>
  └── turn.json
```

`frames.jsonl` 可核对：pcm payload 无 RIFF magic（`52 49 46 46`）；`bad_header` 的头区长度 < 100；binary ACK 字段。

## 10. 验收 Checklist

**正常路径**

- [ ] 握手 Device 三段 + `Action=chatbot`
- [ ] `/register/client` 且 `code==0`（事件带 `correlation_id`）
- [ ] Ready **仅**在 `kind=initial` 的精确序号回显之后
- [ ] `pending_reports` 乱序回显测试通过；无 latest 实现
- [ ] 首次 report 与 keepalive 序号递增；各 `report_echo` 不串 `correlation_id`
- [ ] AudioHeader 100 字节与 golden 一致
- [ ] Seq 从 0，Stage 1→2；payload 为 pcm
- [ ] 真实至少一帧 `'0'` TTS，接收栈未因 UTF-8 失败；`tts_done`；`turn_end_reason=idle`
- [ ] speak 受理时已有 `turn_id`（Reserved）
- [ ] 连接级事件带 `correlation_id`；对话事件带 `turn_id`
- [ ] 音频 `NeedAck==1` 才发 `'4'`（SleepMs=0）；为 0 不 ACK
- [ ] 指令 `need_ack==1` 发 binary ACK（DownlinkType=3，Ack=指令序号）；为 0 不 ACK
- [ ] 代码路径无 `DownlinkAck` 配置查询
- [ ] 未注入超限 → `local_validation_error`，无对应 outbound
- [ ] 配置 `format: wav` 或 `mode: json` 或 `sleep_ms!=0` 被拒绝
- [ ] 周期 report，空闲超过 360s 不掉线
- [ ] `--wait` 在 `first_reply_timeout_sec` 内结束负向用例

**注入（逐行勾选）**

- [ ] `skip_register`：jsonl 证据 + 未预注册 + outbound 有音频 + `expected_server_drop`（WaitingReply 首包超时）
- [ ] `skip_report`：outbound 有音频；**不**要求 drop；允许 TTS
- [ ] `bad_seq`：本地已 Reserved；Seq>=1；`expected_server_drop`
- [ ] `oversize`：payload>51200；`expected_server_drop`
- [ ] `bad_header`：头区 <100 字节；`expected_server_drop`
- [ ] 单测：两次并发 speak，一次 Reserved，一次失败，无双发
- [ ] Stage=4 后 `uplink_end_reason=vad`，补 Stage=2 后仍为 `vad`
- [ ] `cmd/fixture` 可运行并写入 jsonl
- [ ] 不以 mock.go 主路径为验收标准
