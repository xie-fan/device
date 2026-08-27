# 配置树 Registry：环境 → 厂商 → 设备类型 → 设备

日期：2026-08-27。破坏式换新（用户已确认）：`POST /devices` 改为引用树路径，平铺身份字段不再接受。

## 1. 需求定案（用户口径）

- 树：**环境 → 厂商（enterprise）→ 设备类型 → 设备**。
- 环境：名字 + url；url 支持占位符模板。
- 厂商：名称 + 简称；**代入占位符、上线协议用的都是简称**。
- 设备类型：名称 + 简称（协议 `device_type` 用简称）。
- 设备：只有名称（`device_id`），其余全部属性都在设备上。
- 存储：REST CRUD + YAML 落盘（`configs/registry.yaml`），重启保留。
- 兼容：破坏式。设备体内出现 `enterprise` / `device_type` / `server` → 400。

## 2. 契约

### 2.1 数据模型（registry.yaml）

```yaml
environments:
  - name: 本地            # 键，环境内唯一，创建后不可改
    url: ws://127.0.0.1:8089/
    enterprises:
      - name: 演示厂商
        short_name: demo   # 键，环境内唯一，创建后不可改；wire enterprise / url 占位符值
        device_types:
          - name: A3 音箱
            short_name: A3 # 键，厂商内唯一，创建后不可改；wire device_type 值
```

- url 占位符：`{enterprise}`（厂商简称）、`{device_type}`（类型简称）、`{device_id}`。其余 `{...}` → 400。代入后必须是合法 `ws://` / `wss://`。
- 键校验：environment.name、short_name 走 `ValidatePathComponent`（不得含 `/ \ ..` NUL 等）；device_type.short_name 不得 `MH` 前缀（沿用 Seq 例外守卫）。名称（全名）仅要求非空。
- 键不可变：改键 = 删掉重建（有引用时删不掉）。展示 name 与环境 url 可改。

### 2.2 REST（新）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /registry | 整棵树 |
| POST | /registry/environments | `{name,url}` 201；重名 409 |
| PUT | /registry/environments/{env} | 仅 `{url}`；改 url 对 Running 无扰动，下次 start 生效 |
| DELETE | /registry/environments/{env} | 有厂商或被设备引用 → 409；否则 204 |
| POST | /registry/environments/{env}/enterprises | `{name,short_name}` |
| PUT | /registry/environments/{env}/enterprises/{short} | 仅 `{name}` |
| DELETE | 同上 /{short} | 有类型或被设备引用 → 409 |
| POST | /registry/environments/{env}/enterprises/{short}/device_types | `{name,short_name}` |
| PUT | .../device_types/{short} | 仅 `{name}` |
| DELETE | .../device_types/{short} | 被设备引用 → 409 |

父节点不存在 → 404。落盘：临时文件 + rename 原子写，每次变更即写。

### 2.3 设备 API（破坏式改动）

- `POST /devices` 单台：`{ "environment","enterprise","device_type", "device":{ "device_id", ... } }`（enterprise/device_type 填**简称**）。
  - 树引用不存在 → 404；device 体内含 `enterprise`/`device_type`/`server` → 400（同 write_queue_* 门禁）。
  - 解析：`cfg.Enterprise=厂商简称`、`cfg.DeviceType=类型简称`、`cfg.Server.URL=env.url 代入占位符`，再走 LoadPhase2 全量校验。
- 批量：`{ "environment","enterprise","device_type","template_id","count","id_prefix" }`。
- 模板：device 体禁止 `enterprise`/`device_type`/`server`（POST 400；旧模板创建时 400）。
- `PUT /devices/{id}/config`：顶层 `environment`/`enterprise`/`device_type` = **重新挂靠**（可部分给出，缺省沿用当前；仅 Created/Stopped，Running → 409 先判；引用不存在 → 404）。`server` 键 → 400 未知字段。
- `GET /devices` 行 / `GET /devices/{id}` / config 响应：新增 `environment`（环境名）；`enterprise`/`device_type` 仍为简称；config 里 `server.url` 保留为**解析结果只读展示**。
- start / batch start：启动前按设备的环境名从 registry 重解析 url（环境 url 更新 → 重启生效）；解析失败（理论不可达）沿用旧值。
- Phase 1 CLI（cmd/speak + example_device.yaml）**不动**：单机平铺配置不走树。

### 2.4 锁序

`s.mu`（manager）→ `registry.mu`（叶子锁，不回调 api）。删除节点的引用检查与设备创建的 resolve 都在 `s.mu` 临界区内进行，保证「检查-删除」与「解析-建表」互斥。

## 3. 改动点

| 位置 | 内容 |
|------|------|
| manager/registry.go（新） | Registry 类型、Load/原子落盘、CRUD、ResolveURL、校验 |
| api/registry_http.go（新） | /registry 端点；删除前在 s.mu 内查设备引用 |
| api/devices.go | createBody 加 refs；parseDevice 注入解析值 + 三键门禁；PUT 重新挂靠；configPublic + environment |
| api/types.go | managedDevice.envName；deviceView + environment |
| api/lifecycle.go | start/startOne 前重解析 url |
| api/api.go | Options.RegistryPath；New 返回 (http.Handler, error)；挂路由 |
| cmd/manager/main.go | --registry flag；New 错误处理 |
| configs/registry.yaml（新种子）、configs/templates/default_a3.yaml（去三键） |
| ui/index.html + app.js | 新建改级联选择（环境/厂商/类型）+ 节点快速添加；配置表单改挂靠选择；筛选加环境层 |
| docs：architecture.md §4.11+新节、phase2.md §6、phase3.md、README | 契约同步 |

## 4. 测试清单

- [ ] registry CRUD：创建三层 → GET /registry 结构正确；重名 409；父缺失 404
- [ ] 校验：url 未知占位符 400；短名含 `/` 400；device_type 简称 MH 前缀 400
- [ ] 落盘：CRUD 后用同一 RegistryPath 重建 Server，GET /registry 不丢
- [ ] 删除守卫：有子节点 409；被设备引用（含 Stopped）409；清空后 204
- [ ] POST /devices：树引用缺失 404；device 带 enterprise/device_type/server → 400；成功后 config 的 enterprise/device_type=简称、server.url=占位符代入结果
- [ ] 模板：POST /templates 带三键 400；正常模板 + refs 批量创建成功
- [ ] PUT 重新挂靠：Created 换 enterprise 生效（wire 值变）；Running → 409；引用缺失 404；PUT server → 400
- [ ] 环境 url 更新：stop → PUT /registry env url → start，dial 收到新 url
- [ ] GET /devices 行含 environment；现有全量用例改 helpers 后仍绿

## 5. 风险

- 测试面：helpers_test 的 deviceBody/createDevice 是全套用例的入口，改动一次性影响所有 api 测试，需先改 helpers 再跑全量。
- UI 首用体验：registry 为空时无法建设备 → 提交种子 registry.yaml + UI 内联快速添加兜底。
