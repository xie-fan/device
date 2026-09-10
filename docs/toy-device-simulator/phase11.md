# Phase 11：设备册与挂靠分家

**状态：审查通过。实现以本文为准。**

## 1. 为什么

设备定义原本把两件事焊成一条：

- **这台机子是什么** —— device_id、固件版本、网卡、ICCID、音频格式、behavior…
- **它挂在哪** —— 环境 / 厂商 / 设备类型（三级），以及由此派生的 `server.url`

于是测试的入口永远是 device_id：想试某个机型，得先翻名册找一台正好挂在那个机型下的设备。同一类型建 10 台，还要抄 10 份完全相同的属性。

拆开之后：

| | 内容 | 存在哪 | 什么时候定 |
|---|---|---|---|
| **设备册条目** | device_id + 属性 | `data/devices.yaml`，跨重启存活 | 建设备时 |
| **挂靠** | environment / enterprise / device_type + 派生的 url | 只在内存，随 manager 重启消失 | **start 时** |

同一台设备今天可以挂 A 机型跑、停下来明天挂 B 机型跑，设备册条目一个字不动。

> 这一期只搬挂靠，**属性不动**。音频格式、对话模式、协议细节将来归**产品类型**管（另一期），现在把它们挪到「设备类型」上是白搬。合成运行配置的入口 `api/binding.go` 的 `bindDevice` 就是产品类型接进来的那道缝。

## 2. 一台一次只挂一处

真机不可能同时以两种机型在线；租约（phase10）也已保证同一台设备同时只被一个 run 用。

所以 `s.devices` 仍以 `device_id` 为 key，25 条 `/devices/{id}` 路由一条没动，recordings 目录结构不变。挂靠是 `managedDevice` 上一组 start 时写入的运行态字段。

## 3. 契约

### 3.1 建设备：不带挂靠

```
POST /devices  {"device": {...}}                     ← 只有属性
POST /devices  {"template_id":..., "count":..., "id_prefix":...}
```

创建体里出现 `environment` / `enterprise` / `device_type` → **400**。设备体里出现 `enterprise` / `device_type` / `server` → 400（这条一直如此）。

### 3.2 启动：挂靠在这里发生

```
POST /devices/{id}/start        {"environment":"...","enterprise":"...","device_type":"..."}
POST /devices/batch/start       {"device_ids":[...], "environment":..., "enterprise":..., "device_type":...}
scenario 的 batch_start 步骤     同上三个字段
```

三级**必须给全**，没有默认挂靠。缺任一 → 400；树上找不到 → 404（`Resolve` 与「删类型时的引用检查」在同一 `s.mu` 临界区互斥）。挂靠后按完整规则校验（`ValidatePhase2`），`{enterprise}` / `{device_type}` / `{device_id}` 占位符在这一步代入。

### 3.3 PUT /config 不再管挂靠

带 `environment` / `enterprise` / `device_type` 的 PUT → **400**。

理由：挂靠归 start，PUT 改了也会被下次 start 覆盖。留着就是第二条做同一件事的路，还会给人「改成功了」的错觉。

### 3.4 视图

`deviceView` 的 `environment` / `enterprise` / `device_type` 是**运行态**：没 start 的设备这三项是空串，`server.url` 也是空。这不是缺字段，是「还没挂靠」。

### 3.5 删类型的守卫只挡运行中的

设备册条目不再引用任何类型，所以停着的设备挡不住删类型；只有 `starting` / `running` 的算引用。停着的设备在类型被删后 start 会自己因 `Resolve` 失败报 404。

### 3.6 历史记住这次挂成了什么

`turn.json` 新增 `enterprise` / `device_type` / `server_url`，`GET /devices/{id}/instances` 的每一项跟着带出来。

同一个 device_id 昨天是 A 机型、今天是 B 机型，不记下来回看时分不出来。老录音没有这几个字段，读回来是空串。

### 3.7 校验分层

| 函数 | 校验什么 | 用在哪 |
|---|---|---|
| `config.ValidateBookEntry` | 只有属性 | 建设备、加载 devices.yaml、PUT /definition |
| `config.ValidatePhase2` | 属性 + 身份三级 + server.url | start 时（挂靠之后）、PUT /config（已挂靠的） |
| `config.WithoutBinding` | 抹掉三级与 url | 落盘、比较「当前值是否偏离定义」 |

`overridden()` 与 `reset` 只比、只回滚**属性**：挂靠是这次运行的身份，不是「临时修改」。

## 4. 迁移

`devices.yaml` 不再有 `environment`，设备体不再有 `enterprise` / `device_type` / `server`。

**旧文件不需要迁移代码**：这些字段从结构体上摘掉后，yaml 解析直接忽略；加载时再过一道 `WithoutBinding` 兜底。效果就是老设备全部变成「未挂靠」——符合「不留默认挂靠」的决定。下一次落盘自动写成新格式。

## 5. simctl

三级从「筛设备的条件」变成「这次挂成什么」，必须给全：

```
simctl run <device_id> --env E --enterprise P --device-type T --asset X   # 就那一台
simctl run             --env E --enterprise P --device-type T --asset X   # 整册随机一台
simctl run --count 3   ...                                                # 随机三台
simctl run --count 0   ...                                                # 整册全跑
```

phase10 那条「过滤粒度决定跑几台」**作废**：粒度不存在了，三级要么给全要么报错，批量改由 `--count` 承担。租约、随机挑一台、只对争用跳台都保留。

`devices` 动词的 `--env` / `--enterprise` / `--device-type` 仍是筛选，筛的是**当前挂靠**——只对跑着的设备有意义。

## 6. UI

- 新建设备抽屉去掉「挂到配置树上」那一步。
- 调试台的启动按钮前多一条**挂靠**：三个紧凑下拉，和「厂商与设备类型」页共用同一份 `state.regSel`，所以在哪边选都一样。没选全就点启动会提示。
- 批量启动与场景的 `batch_start` 用同一份挂靠。

## 7. 不在本期

- **产品 / 产品类型**：将来由它规定音频格式、对话模式（按键 / 连续 / 唤醒词）和协议细节，基本顶替现在的「设备」概念。接入点是 `bindDevice`。
- **同一 device_id 同时挂多处**：需要把运行态 key 从 `device_id` 换成四元组，25 条路由、UI、历史、租约全要动，且真机不成立。
