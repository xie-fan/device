# Phase 10：按设备类型选台 + 设备租约

**状态：审查通过。实现以本文为准。**

本期只改**测试入口**。设备定义的存储模型一个字没动：每台设备仍各自带全套属性，`device_type` 仍来自配置树引用（phase2.md §6.10）。「设备属性上移到设备类型」不在本期。

## 1. 为什么

跑一次对话测试必须先知道一个具体的 `device_id`。人真正关心的是「某一类设备」——要试的是 MH8W 这个机型，用池子里哪一台无所谓。于是每次都要先 `simctl devices` 翻一遍、挑一个 id 抄进命令行。

同一个设备类型下已有的设备定义天然就是池子，缺的只是「随机挑一台」和「两个并发 run 不撞车」。

## 2. 选择语义：过滤粒度决定行为

`simctl run` 选几台，完全由「给了哪些过滤」决定，**没有 `--random` / `--all` 这类开关**——两套语义并存是 bug 的温床。

| 给的条件 | 跑几台 |
|---|---|
| 位置参数 `device_id` | 就那一台 |
| 只给到 `--device-type` | 从该类型下**随机挑一台** |
| 只给 `--env` / `--enterprise` | 命中的**全部** |
| 什么都不给 | 全部 |

`--device-type` 与 `--env` / `--enterprise` 同时给时，合取照旧，最细的一档是 device-type，所以走随机。

不变的部分：过滤仍在客户端做，**不给服务端加过滤参数**（phase9 §3 那条原则原样有效）；**选中 0 台仍是报错退出**，绝不返回空数组；输出**始终是数组**。

`--parallel` 只在批量档有意义，随机档只跑一台，静默忽略。

## 3. 租约：run 之间的协作锁

### 3.1 端点

```
POST   /devices/{id}/lease
       body {"owner":"...", "ttl_sec":180, "steal":false}   三项皆可省
       200 {"device_id","lease_id","expires_at","device":{deviceView}}
       409 {"error":"lease_held","device_id","owner","expires_at"}
       404 {"error":"device 不存在"}

DELETE /devices/{id}/lease?lease_id=lse_xxx
       200 {"device_id","released":true|false}
       404 {"error":"device 不存在"}
```

- `lease_id` 是**能力凭证**（谁拿到谁能释放），用 `crypto/rand` 生成，**绝不出现在 `deviceView` 里**——否则谁 `GET /devices` 一下就能释放别人的租约。
- 释放时 `lease_id` 对不上 → `released:false` 但仍 **200**。manager 重启后拿着陈旧 id 来释放不该报错。
- `steal:true` 无视现有持有者直接改签，用于持租进程已死（SIGKILL / 掉电 / 换机器）。CLI 侧是 `--force`。
- `deviceView` 新增两个平铺字段：`leased_until`（RFC3339，空串 = 未占用）、`lease_owner`。

### 3.2 TTL 与过期

默认 **180s**。一次 run 的上界 ≈ `wait_ready` 30s + `speak_and_wait` 的 `WaitBudget`（30s 素材约 70s）≈ 105s，留一倍余量。`ttl_sec` 可覆盖，上限 10 分钟。

**惰性过期**：过期只在 acquire / `deviceView` / release 三处被观察，全都在 `s.mu` 里，`leaseHeldLocked` 一个函数就地清账。不起后台清扫协程。

**不续租**。真出现超过 TTL 的 run，POST 时带上原 `lease_id` 视作续期即可（三行）。

### 3.3 租约不落盘

`data/devices.yaml` 存的是**定义**，`instanceID` / `state` / `gen` 这些运行态一个都没进去。租约是运行态。而且重启后的租约必然是假的——所有设备回到 `created`、持租进程早已死，恢复它只会在 TTL 到期前挡住所有人。

### 3.4 租约是协作锁，不是排他锁

**UI、`/start`、`/stop`、`/speak`、`/speak_and_wait`、`/config`、scenario 一律不看租约。**

不变式「两个并发 run 不撞同一台」只需要两个 run 都走租约就成立。挡住 UI 要给每个变更端点加 `lease_id` 参数、`ui/app.js` 全跟着改、还得给人一个破锁按钮——爆炸半径十倍于收益。

这也和本项目的既有姿态一致：`overridden` / `--dirty`（phase9 §3）是**提示 + 显式放行**，不是硬锁。

不挡的最坏后果是可控降级：UI 在 run 中途 speak，撞上 core 的槽 → 409 或排队，不是数据损坏。

### 3.5 租约 vs core 的槽

|  | core 槽（`core/slot.go`） | 租约 |
|---|---|---|
| 保护 | 一台实例上的**一轮** | 一次 run 对一个 device_id 的**整段占用** |
| 生命期 | 几秒到几十秒 | 整个 run（reset / start / wait_ready / speak / 读 turn） |
| 谁释放 | core 自己 | 客户端 `defer`，或 TTL |
| 锁 | core 的 `device_mu` | api 的 `s.mu` |

两者都是**非阻塞 try-acquire**，任何一个失败都立刻返回，构不成互相等待。

**实现约束：lease handler 全程只碰 `s.mu`，一次都不调 core。** 这是锁序安全的全部依据。

## 4. CLI 行为

- 跑任何一台之前先 `POST /lease`，跑完 `defer` 还。释放失败不检查——TTL 兜底。
- 租约 200 回的那份 `deviceView` 直接喂给后续的 start / speak，顺带消掉一个既有竞态：`GET /devices` 到 `start` 之间别人把设备起起来了，会拿到 409「仅 Created/Stopped 可 start」。
- **随机档**：洗牌（`math/rand/v2`）后逐台试租，**409 才换下一台；跑失败绝不换台**。一台设备真起不来就该把错报出来——安静换一台跑成功会把故障藏起来，`--dirty` 门禁和 `last_error` 那两个诊断全白费。全部被占 → 报错退出 1。
- **批量档**：被占的那台作为 `{"device_id":..., "error":"lease_held…"}` 元素，**不跳过**。数组要和选中集一一对应，否则读的人分不清「这台没跑」和「这台跑了没回话」。
- **点名档**：那台被占直接报错，不换台。
- 随机用 `math/rand/v2`（要的是分散不是不可预测）；ID 生成继续用 `crypto/rand`，两者用途不同，别混。

## 5. 不在本期

- **设备属性上移到设备类型**（建设备 = 往池子里加个 id）。本期只改入口。
- **`{"error":"槽已占用"}` 改成机器可读 code**。有了租约 run 基本碰不到它，剩下的触发路径是 UI 与 run 互踩，simctl 对它不做分支判断。它是对外契约变更，单独一个 commit。真要改是在 api 边界映射（`errors.Is(err, core.ErrSlotOccupied)` → `slot_occupied`），`api/speak.go` 三处 `err.Error()` 都要照顾到。
- **UI 显示「被占」徽章**。`leased_until` / `lease_owner` 已经在 `deviceView` 里，加徽章是锦上添花。
