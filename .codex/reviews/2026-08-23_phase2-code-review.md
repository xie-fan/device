# Phase 2 代码审核报告

- 审核对象：分支 `phase2`，提交 `d3f9a00`（相对 `main`：54 文件、+7793 行；生产 4489 / 测试 3304）
- 契约基线：`docs/toy-device-simulator/phase2.md`、`docs/toy-device-simulator/architecture.md`
- 计划基线：`.codex/plans/current/2026-08-23_phase2.md`
- 审核日期：2026-08-23
- 验证方式：全量 `go test ./...` 通过；`go build`/`go vet` 干净；针对可疑点写了 4 个一次性探针测试实测（用后已删除，工作区干净）

---

## 0. 结论

| 维度 | 评级 | 一句话 |
|------|------|--------|
| 规划实现程度 | **A-** | 计划清单 67 个用例名 **全部存在**；`§6` 全部端点可调用；缺 `configs/manager.yaml` 与 `configs/templates/` 两个交付物 |
| 功能完善度 | **B** | 主干契约（游标 / tombstone / 三段收口 / 单一队列 / JSON ACK）落得扎实；有 1 个功能性缺陷 + 6 处契约偏差 |
| 代码健壮度 | **B-** | 锁序、waiter 一次性消费、notify 传播都对；但有 1 个路径穿越、1 个数据竞争、若干无界增长 |
| 过度设计 | **轻微** | 无架构级过度设计；有 ~3 处"为测试而生"的死代码 |
| 代码简洁度 | **C+** | `api/devices.go` 300 行手写 allowlist、两份重复的 `valueHasKey`、`scenario` 包与 HTTP 层职责重叠 |

**放行建议**：修掉 P0（3 项）后可合入；P1 建议同批处理。

---

## 1. 规划实现程度

### 1.1 测试清单（计划 §测试清单）

计划列了 67 个具名用例，代码中共 188 个 `TestXxx`。逐名比对结果：

```
计划要求 67 个 → 实际缺失 0 个
```

Phase 1 回归（第 68 项）也保持全绿。这一项做得比多数交付都干净。

**测试质量抽检**（不是走过场）：

- `TestWaitEventTimeoutAfterRemoveFailsWaitsNotify`（`api/phase2_holes_test.go:15`）真的构造了"waiter 已被 `appendEventLocked` 摘表、但 `NotifyHTTP` 尚未发生"的时间窗，并断言此时**不得**提前返回、更不得返回字段全空的 200。
- `TestWaitReadyAfterSpeakableTakenWaitsNotifyNot504` 同样卡在 Phase C 摘表与唤醒之间。
- 失败信息全部中文且写明"应该怎样"，符合既有风格。

### 1.2 交付物落点（计划 §代码落点）

| 计划落点 | 状态 |
|----------|------|
| `manager/` Manager YAML、permit、tombstone | ⚠️ 只有 YAML；permit 在 `api/devices.go:889`、tombstone 在 `api/lifecycle.go` |
| `api/` HTTP + 事件 WS | ✅ |
| `cmd/manager/` 进程入口 | ✅ |
| `scenario/` 运行器 | ⚠️ 真正的运行器在 `api/scenario_http.go`，`scenario/` 只剩解析辅助 |
| `protocol/` JSON ACK | ✅ |
| `core/` playing_mode 热更新、录音 `instance_id`、JSON ACK 路径 | ✅ |
| **`configs/manager.yaml`** | ❌ **未交付** |
| **`configs/templates/`** | ❌ **未交付**（目录不存在） |
| 设备 YAML 禁止 `write_queue_*` | ✅ |

`cmd/manager` 强制 `--config`，而仓库里没有任何一份 manager YAML 样例，等于交付的二进制**开箱跑不起来**（`docs/.../phase2.md §5` 有全文，抄一份即可）。

---

## 2. 功能完善度：缺陷与契约偏差

### P0-1 事件 WS 订阅跨代失聪（功能性缺陷）

**现象**：live WS 订阅登记在 `DeviceInstance.hubSubs`（`core/device.go:142`），而 `DeviceInstance` **每代重建**（`api/lifecycle.go:36/386`）；`EventLog` 却是跨代共享的（`api/lifecycle.go:57` 把同一个 `d.log` 传给每一代）。于是 `stop → start` 之后，旧订阅还挂在已收口的那一代实例上，新一代的 `appendEventLocked` 只投递给新实例的空 hub。

**实测**（探针）：订阅 → `POST stop` → `POST start` → `wait_ready`：

```
WS 收到：[connection_stopped]        ← 之后再无任何事件
对照 /wait ready(after=6) → 200 seq=13   ← HTTP 侧看得见新一代事件
读循环：i/o timeout（连接一直开着，不是被关闭）
```

即客户端**静默失聪且永不断开**（空闲 Ping 还会一直把它保活）。而契约把 WS 定义为 **instance 级**（`architecture.md §4.2` 路由表、`phase2.md §6.7` 的 `?instance_id=` 必填），同一 instance 跨代必须继续送。

**修法**：把 `hubSubs` 连同 `speakableWaiters` 一起上提到与 `EventLog` 同寿命的对象（例如 `managedDevice` 或一个 instance 级 hub 结构），`DeviceInstance` 只持引用；或退一步：Phase C 里 `requestCloseAllLocked(WSDrain)`，让旧订阅显式断开而不是装死（下策，但至少不撒谎）。

---

### P0-2 `/templates/{id}` 路径穿越（任意 `.yaml` 读 + 删）

`api/devices.go:857 handleGetTemplate` / `:867 handleDeleteTemplate` / `:826 readTemplate` 直接把 `r.PathValue("id")` 拼进文件名，**没有** `config.ValidatePathComponent`（`POST /templates` 是有的，`:790` 附近）。

Go 1.22 `ServeMux` 会把 `%2e%2e%2f` **逐段反转义**后交给 `PathValue`，`..` 不会被 `net/http` 的路径清洗拦下。实测：

```
GET    /templates/..%2fsecret → 200 {"device":{"device_id":"leaked"},"template_id":"../secret"}
DELETE /templates/..%2fsecret → 204，磁盘上的 secret.yaml 已被删除
```

即对进程用户可读写的**任意 `.yaml` 文件**构成读取与删除原语。直接违反 `architecture.md §4.16`「禁止 HTTP 读任意服务器路径」这条硬约束。

**修法**：`handleGetTemplate` / `handleDeleteTemplate` / `readTemplate` 入口统一 `config.ValidatePathComponent(id)`，非法 400。顺带 `readTemplate` 也被 `POST /devices` 的 `template_id` 复用，一处修全覆盖。

---

### P0-3 `speak_and_wait` 默认超时写死 30s

`api/speak.go:149`：

```go
timeout := 30 * time.Second
```

`architecture.md §4.4` 的原文是：「等待预算：`upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。**禁止默认写死 30s**」。core 已经备好 `DeviceInstance.WaitBudgetFor(pcmBytes)`（`core/turn.go:263`），Phase 1 CLI 正是这么用的（`cmd/speak/main.go:97`）。

讽刺的是 `core/matrix_test.go:115 TestWaitBudgetIsNotHardcoded30s` 只测了纯函数，HTTP 这条路径从它旁边溜过去了。

**修法**：`timeout = inst.WaitBudgetFor(len(pcm))`；`timeout_sec` 显式给出时才覆盖。同时给 `/speak_and_wait` 补一个"设备把 idle 调大后默认超时也跟着变大"的用例。

---

### P1-1 批量全失败的状态码规则反了

`phase2.md §6.5`：「全失败**有** 429→429；全 409→409；其它 400」。

`api/lifecycle.go:438` 要求**全部**是 429 才回 429：

```go
all429, all409 := true, true
```

实测（`max_connections=1`，一台 Running 一台 Created）：

```
failed=[409, 429] → HTTP 400   ← 契约要求 429
```

`TestBatchAllPermitExhausted429` 只覆盖了纯 429 的情形，所以没抓到。

---

### P1-2 `id_prefix` 拼接格式与契约示例不符

`api/devices.go:136`：`body.IDPrefix + strconv.Itoa(i)`。

契约 `§6.2` 的示例是 `id_prefix:"sim"` → `device_ids:["sim_1"]`；实测得到 `["sim1","sim2"]`。Agent 按文档写的客户端会对不上 ID。二选一：改代码加下划线，或在文档里把示例改成 `sim1`（但文档冻结，故应改代码）。

---

### P1-3 503 全链路没有实现

`§3` 错误码表里的 503（"outbound buffer 满且尚未成功响应"）在 `api/` 与 `core/` 里**一次都没出现**。唯一可能命中的同步 HTTP 路径是 `POST /report`：`core/report_net.go` 的 `ManualReport` 走 `enqueueOrFinalize`，错误被吞掉，照样回 202。

---

### P1-4 Manager 配置项定义了不用

| 键 | 状态 |
|----|------|
| `default_stagger_ms` | `manager.Load` 会填默认值 50，但 `handleBatchStart`（`api/lifecycle.go:352`）只认 body 里的 `stagger_ms`，省略即 0 错峰。**配置形同虚设** |
| `per_device_buffer_bytes` | 全仓库无任何读取点 |

`TestManagerDefaultStaggerAndPermitsLoad` 只断言 Load 后的字段值，测不到"有没有人用"。

---

### P1-5 tombstone / scenario runs / turn 索引全部无界增长

- `s.tombs`（`api/api.go:33`）只在读取时比对 `expires`，**没有任何清扫**；删过的设备永久占内存。
- `s.runs` 同理，Scenario 跑完不回收。
- `core.DeviceInstance.turnDone` / `turnTerm`（`core/device.go:127-128`）每个 Turn 一条，永不删除。
- 录音目录的 TTL（`§6.8`「录音文件 TTL 与 event_log_ttl_hours 相同」）没有实现清理。契约用的是"过期可删"，所以这条算**可选未做**，但内存三处是实打实的泄漏。

---

### P2 级偏差（不阻塞，记账）

| # | 位置 | 说明 |
|---|------|------|
| a | `api/lifecycle.go:395` `handleBatchStop/Delete` | 成功行的 `conn_generation` 恒为 0（`batchOK` 无 `omitempty`）；`succeeded`/`failed` 为空时序列化成 `null` 而非 `[]`，严格客户端会崩 |
| b | `api/lifecycle.go:352/398/420` | `_ = json.NewDecoder(r.Body).Decode(&body)` 吞掉解析错误，畸形 body 当空数组处理并回 202/200，应为 400 |
| c | `api/devices.go:844 handleListTemplates` | 不套用 `configs/templates` 默认目录（`readTemplate`/`handlePostTemplate` 都套了），`TemplatesDir==""` 时列空 |
| d | `api/devices.go:867` | 模板不存在也回 204，无法区分"删了"与"本来就没有" |
| e | `core/interrupt.go:32` | 先判 `finalize_started` 再判 `turn_mismatch`，`§6.8` 表里的顺序相反（都是 409，仅 body 不同） |
| f | `api/types.go:104 syncRunning` | `instance_state` 只在 speakable 时才置 `running`，于是"Running + registering"这个契约里存在的组合永远观察不到 |
| g | `core/finalize.go:139` | stop 与 delete 都用 `reason="user_stop"`；`§4.12` 规定 delete 应为 `user_delete`（优先级 `user_delete > user_stop`） |
| h | `api/assets.go:22` | `ParseMultipartForm(32<<20)` 在校验 `max_asset_bytes`（默认 10 MB）**之前**就落盘/占内存，无 `http.MaxBytesReader` 兜底 |

---

## 3. 健壮度

### 做对了的（值得记账）

- **锁序**：`s.mu(manager) → deviceMu → connMu → reportMu` 全链路一致；`finishCritical` 一律在 `deviceMu.Unlock()` **之后**调用（`Interrupt` 用 `defer func(){ Unlock(); finishCritical() }()` 的闭包捕获写法是对的），`onActivity` 回调用 `go fn()` 规避回锁。没有发现锁倒置。
- **waiter 一次性消费**：`EventLog.FindOrRegisterWaiter` 检查与登记同一临界区；`AppendLocked` 摘表并把 `wakes` 带出锁外；超时路径 `RemoveWaiter` 试消费失败就转等通知。契约里最容易写错的"日志已有事件却等到 504"确实被堵死了。
- **`EventNotify` 传播**：全仓库扫过，**没有任何一处**丢弃 `appendEventLocked` 的返回值（这正是 `d3f9a00` 修掉的 `eventNotifyOf(tn)` 那一类 bug）。
- **WS 关停三态**：`open/drain/abort` 的所有权分工（`requestCloseLocked` 持锁禁 IO、`requestClose`/`finishAbort`/`abort` 不持锁）与 `core/wshub.go` 完全对齐；`inbox` 只 `close` 一次且先 `delete(hubSubs)` 再 `close`，杜绝了"向已关闭 channel 投递"的 panic。
- **空闲半开**：`waitIdleWS`（`api/ws.go`）用两个一次性 timer、`WriteControl(Ping, nil, idleDeadline)`、全程不碰 `SetReadDeadline`，总预算恰好一个 T。逻辑是对的 —— 只是**没有任何测试**（见下）。

### 风险点

**H-1 数据竞争：`d.cfg` 无保护写**（`core/report_net.go:94` vs `core/device.go:206`）

```go
// 写：ManualReport 持 deviceMu
d.cfg.PlayingMode = *playingMode
// 读：无锁整结构体拷贝，api/speak.go:137 在另一 goroutine 调用
func (d *DeviceInstance) Config() config.Device { return d.cfg }
```

`-race` 必报。`ThrottleLast()`（`core/device.go:275`）同类问题（写在 `enqueueAckLocked` 持锁路径）。
修法：`Config()` 走 `deviceMu`，或把 `PlayingMode` 单独拆成 `atomic.Int64`。

**H-2 本机无法跑 `-race`**：`CGO_ENABLED=1 go test -race` 报 `gcc not found`。对一份把并发正确性当成主要契约的代码，**竞态检测器从未在这份实现上跑过**。建议在 CI 或装 MinGW 的机器上补一次全量 `-race`，这是性价比最高的一步。

**H-3 `waitEventTimeout` 的无限阻塞兜底**（`api/wait.go:123`）

```go
still := log.RemoveWaiter(ch)
if !still {
    got := <-ch   // 无超时
```

今天安全（第 3 节已确认没有丢 notify 的调用点），但任何一个未来新增的 `appendEventLocked` 调用点忘了传播 `EventNotify`，代价就从"多等到 504"升级为"HTTP 处理协程永久泄漏"。加一个 `select { case got := <-ch: ...; case <-time.After(grace): return 504 }` 即可把故障降级。

**H-4 全局 `s.mu` 串行化所有 HTTP**：`handleListDevices` 持 `s.mu` 逐台调 `d.inst.ConnectionState()`（要 `deviceMu`）。当前 `deviceMu` 临界区内没有 IO，所以是安全的；但这条"管理面单锁 + 锁内跨对象取子锁"的形状，任何一次未来的"锁内做点小 IO"都会把整个 manager 卡死。属设计取舍，记账即可。

**H-5 `phaseB` 5 ms 轮询**（`core/finalize.go:90-99`）：已有 `writePumpCond`，却用 `time.Sleep` 轮询排空。功能正确，但在 32 设备同时收口时是 32 个自旋协程。

**H-6 `go d.noteStage2Sent(f.TurnID)`**（`core/conn.go:168`）：Stage=2 写出后异步补 `uplink_end_reason=stage2`，与同时发生的 `/interrupt` 竞争"谁先写空值"。`SetUplinkEnd` 是先到先得，两种结果都不违约，但引入了本可避免的不确定性 —— 直接在 `onWritten` 里同步取锁写即可（`onWritten` 本就不持 `deviceMu`）。

### 测试盲区

| 盲区 | 说明 |
|------|------|
| **WS 空闲半开** | `§9.2` 明确要求验收"不回 Pong / TCP 黑洞 → 进入空闲起 ≤T 内 `finishAbort` 且 socket 已关；Pong 成功后跨多个 T 窗口仍 live"。`api/` 下 **无任何 Ping/Pong/idle 用例**。这是整个交付里最绕的一段并发代码（`waitIdleWS` 的 3 路 select + 两个一次性 timer），零覆盖。计划清单本身就漏了这条，属规划缺口传导。 |
| **inbox 过载 → abort** | `deliverHubLocked` 的 live 上限 256 / catchup 上限 `event_log_max_entries` 分支无用例。 |
| **跨代 WS** | 即 P0-1；无用例，所以缺陷躲过了 188 个测试。 |
| **批量混合失败** | 见 P1-1。 |

---

## 4. 过度设计

整体**不算过度设计**：没有多余的抽象层、没有接口膨胀、没有提前为 Phase 3/4 铺路。问题集中在"为通过某条用例而生、生产路径不走"的死代码：

| # | 位置 | 说明 |
|---|------|------|
| 1 | `manager/device_yaml.go`（59 行） | `LoadDeviceYAML` **只被自己的测试调用**。生产路径（`POST /devices` / `POST /templates`）用的是 `api/devices.go:27` 里**另一份**几乎一样的 `jsonHasKey`+`valueHasKey`。等于为 `TestDeviceYAMLWithWriteQueueFieldsRejectedInPhase2Load` 造了一条平行实现。 |
| 2 | `core/slot.go:114 NotifyCompletion` | 只被 `slot_test.go` 调用，函数体本身是无意义表达式：`s.woke = s.woke \|\| !s.held && true`。建议直接删。 |
| 3 | `protocol/ack.go:67 CommandBinaryAck` | 生产无引用（`enqueueAckLocked` 统一用 `AudioBinaryAck(seq, downlinkType, code)`），只剩测试在用。 |
| 4 | `core/device.go:99 connGeneration` | 只在 `core/conn.go:55` 写死为 1，无人读。代际实际由 api 层维护。 |
| 5 | `protocol.SleepThrottle` + `ThrottleLast()` | 缓存了设备**自己配置**的 `sleep_ms`，没有任何消费者。为满足"0 不写缓存"这条单元断言而存在的装饰。 |
| 6 | `api/api.go:20 AfterAssetStat` | 生产 `Options` 里的测试钩子。契约确实要求测"拷贝窗口内 DELETE"，钩子是务实选择，但应加注释标明 test-only。 |

---

## 5. 简洁度

**C+**。核心状态机部分写得紧凑克制（`core/wshub.go` 384 行覆盖了整套 close_mode 语义，密度合适）。扣分在 HTTP 层：

1. **`api/devices.go` 的 PUT allowlist ≈ 300 行手写映射**（`:270`–`:740`）。`applyPutAllowlist` / `applyPutAudio` / `applyPutServer` / `applyPutUUID` / `applyPutAck` / `applyPutBehavior` / `applyPutRecording` 逐字段 `v.(string)` + `jsonToInt` + 拼错误串。
   讽刺的是同文件 `:54 parseDevice` 已经示范了更短的写法：**JSON → map → `yaml.Marshal` → `config.LoadPhase2`**，复用既有校验。PUT 完全可以走"取当前 cfg → 用指针字段结构体 `json.Unmarshal` 覆盖 → `ValidatePhase2`"，约 60 行搞定，还顺带消灭手写类型断言引入的分歧风险。

2. **`valueHasKey` 两份**（`api/devices.go:35` 与 `manager/device_yaml.go:26`），逻辑相同、签名相同、一份没人用。

3. **`scenario` 包与 `api/scenario_http.go` 职责重叠**：`scenario.Start(raw)` 解析一遍 + 校验 + 生成 run_id，返回后 `handleScenarioRun`（`api/scenario_http.go:20-35`）把**同一份 raw 再 `json.Unmarshal` 一遍**、再 `ApplyDefaults` 一遍。`scenario.Start` 实际只贡献了 run_id 生成。要么让 `Start` 返回 `Spec`，要么把它降级成 `NewRunID()`。

4. **`api/scenario_http.go:257 internalJSON` 用 `net/http/httptest` 发内部请求**。可读性上确实省事，但把测试基建编进生产二进制、每步 Scenario 走一遍完整 mux + JSON 编解码。改调 `s.startOne` / `s.waitReady` / `s.doSpeak` 这类已经存在的内部方法更直接（`waitReady` 已经是拆好的纯函数，`execBatchStart` 里也确实直接调了它 —— 说明这条路走得通，只是没走完）。

5. `gofmt -l` 有 4 个文件未格式化，其中 `api/query_test.go` 是本次新增（尾部多余空行）。

---

## 6. 修复优先级

**P0（合入前）**
1. WS 订阅跨代失聪 —— hub 上提到 instance 级（§2 P0-1）
2. `/templates/{id}` GET/DELETE 补 `ValidatePathComponent`（§2 P0-2）
3. `speak_and_wait` 改用 `WaitBudgetFor`，删掉 30s（§2 P0-3）

**P1（同批）**
4. 批量全失败："有 429 → 429"（§2 P1-1）
5. `id_prefix` 拼接改 `prefix + "_" + i`（§2 P1-2）
6. 补 WS 空闲半开用例 + inbox 过载用例 + 跨代 WS 用例（§3 测试盲区）
7. 在带 gcc 的环境跑一次全量 `go test -race ./...`（§3 H-2）
8. 补 `configs/manager.yaml` 与 `configs/templates/.gitkeep`（§1.2）
9. `Config()` 加锁或拆 `PlayingMode`（§3 H-1）

**P2（择机）**
10. tombstone / runs / turnDone 加清扫（§2 P1-5）
11. `POST /report` 队列满回 503（§2 P1-3）
12. `default_stagger_ms` 接进 batch 默认值（§2 P1-4）
13. PUT allowlist 重写为结构体覆盖（§5.1）；删 §4 的 6 处死代码
14. `waitEventTimeout` 的 `<-ch` 加兜底超时（§3 H-3）

---

## 7. 审核方法与边界

**做过的**：全量 `go test ./...`（13 包全绿，api 10.7s）、`go build`、`go vet`、`gofmt -l`；逐行读了 `api/` 全部 9 个生产文件与 `core/` 的 Phase 2 增量（`wshub.go` / `events.go` / `interrupt.go` / `finalize.go` / `turn.go` / `device.go` / `report_net.go` 及 `downlink.go` 的 diff）；把 `phase2.md §3/§4/§5/§6.1–6.9/§7` 与 `architecture.md §4.1–4.16/§5` 逐条对照实现；对 4 个可疑点写了探针测试实测（模板穿越、跨代 WS、批量混合失败、id_prefix），结论均已复现，探针已删除。

**没做的**：
- `-race`（本机缺 gcc，见 H-2）—— 因此第 3 节关于竞态的结论来自代码推演而非工具证据。
- 未接真实 core 服务端联调（§9.1 的"浏览器上传 WAV → speak → 下载 downlink"人工验收未执行）。
- Phase 1 既有文件（`outbound.go` / `matrix.go` / `audio.go` / `recording/`）本次未改，只在与 Phase 2 交互处审。
- A/B/C 三台 ACK 组合（`§7`）的真机验收未执行，仅确认编码路径正确（binary 指令 `DownlinkType=3`、JSON 指令带原 topic、JSON 音频带 uuid）。
