# 玩具设备模拟器规划审查（v15）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v15/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v15 设计正文。修订见 `docs/toy-device-simulator-docs-v16/`。

**决策：NEEDS_CHANGES**

**迭代原则（本轮起写进规划）：** 新版本只**补全**已有契约与正确性；**不得**给某阶段增加产品功能，除非没有该项就无法达到该阶段原定目标。禁止把 UI/REST 提前到 Phase 1，禁止把 speak backlog 提前到 Phase 2。`instance_id` 已为删除重用所必须，本轮只把它接到游标上，不新开资源类型。

阶段边界不移动。

---

## 1. 阻塞问题

### B1. 不再可独立实施

写了「同前版实施」；Phase 2 删掉列表/模板/Turn/frames/录音 URL/batch 全失败规则。

**改为：** 游标规则写全。恢复 v14 已有 API 表，每条带上已有的 `instance_id`。录音路径写死，不是「建议」。

### B2. Stage 3 复核与泵禁回锁冲突

**改为：** 删除「写出前持 device 锁看 Turn」。证明 `finalize_started` 后 **只有 Phase C** 能 Terminal（完成矩阵、interrupt、speak 均不可）。BeginClose 把 Stage 3 放进泵内 token；泵协程不回锁。

### B3. 省略 instance_id 无法防串世系；live/tombstone 409 与回放冲突

**改为：** `/wait`、WS、`GET .../events` **必填** `instance_id`。按该 ID **精确**选 live 或 tombstone。device_id 不一致 → 404。不再「live 不匹配再查 tombstone」。

### B4. wait_ready 未绑 generation

**改为：** 已有 `wait_ready`/`speakable` 请求体必填 `instance_id` + `conn_generation`。该代已 Stopped/Deleted 且从未 speakable → **409** `generation_gone`，不 504。不新发明端点。

### B5. 拷贝无线性化点

**改为：** 拷贝前快照 `audio_fp` + `conn_generation` + 资产 `epoch`；拷贝后 CAS 前复验同一快照。DELETE 与读快照争 `epoch`：拷贝窗口内 epoch 变化 → 404。不新增资产 API。

---

## 2. 非阻塞

v15 对 v14 的 8 点均有对症补丁；问题是契约被写薄。outbound buffer vs speak backlog 命名保留。

---

## 3. 验证缺口

无实现。修订后验收：完整 API、泵不回锁、强制 instance_id 游标、wait_ready 绑 generation、epoch 线性化。
