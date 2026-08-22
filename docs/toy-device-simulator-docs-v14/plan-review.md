# 玩具设备模拟器规划审查（v14）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v14/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v14 设计正文。修订见 `docs/toy-device-simulator-docs-v15/`。

**决策：NEEDS_CHANGES**

阶段边界 **不移动**：P1 单设备 CLI；P2 批量+API+Scenario；P3 UI；P4 按需（含 **speak backlog**，不是 writePump outbound buffer）。

---

## 1. 阻塞问题

### B1. Scenario 在 Ready 前 speak

`batch_start` HTTP 202 不表示可 speak。示例下一步立即 speak。Starting 时 speak 无错误码。

**改为：** HTTP start 仍 202。Scenario 的 `batch_start` **默认阻塞到 speakable**。补 `wait_ready` / `event_type=speakable`。未就绪 speak → **409**。

### B2. speak 示例违反互斥

同时给了顶层 `asset_id` 与 `stream`。拆成两个合法样例；同时给两者 → 400。

### B3. 资产拷贝失败未回滚 CAS/permit

**改为：先拷贝 PCM，再 CAS + permit。** 拷贝失败不占槽、不拿 permit。

### B4. Stopped→Starting 代际未复位

BeginClose 的 `closing=true` 属于旧 writePump。start 必须 **新建** writePump/连接，并复位 pending、timer、`uplink_frozen`、finalize_*。禁止在已 closing 的泵上把标志拨回去。

### B5. Phase B 迟到下行可使 Turn Terminal 仍发出 Stage 3

**改为：** `finalize_started` 后禁止完成矩阵把该 Turn 置 Terminal。写出 Stage 3 前再校验；已 Terminal 则撤销 finalFrame。Phase C 用 `connection_lost` 收口。

### B6. Created 的 stop/delete 无唯一答案

Created 无连接/permit。**stop：** Created→Stopped，200，不走 finalizer，无 `connection_stopped`。**delete：** 直接 tombstone + `device_deleted`，200。不是 409。

### B7.（P2）删除后事件/游标

允许 `device_id` 重用。每次创建新 `instance_id`。`event_seq` 按 instance。删除前写入 `device_deleted`，log 进 tombstone。新旧 cursor 用 `instance_id` 隔离。

### B8.（P2）PUT 字段过宽

字段级 allowlist：音频/身份/ACK 仅 Created/Stopped；`playing_mode` Running 时只许 `POST /report`。

---

## 2. 非阻塞

- 运输层称 **outbound buffer（writePump）**；Phase 4 称 **speak backlog**。禁止再用光秃的「queue」同时指两者。
- v14 的 join-wait、BeginClose、WAV、阶段边界必须保留。

---

## 3. 验证缺口

无实现。修订后验收：Scenario 等 speakable、非法 speak body、拷贝失败不占槽、新 generation 新泵、finalizer∥迟到下行、Created stop/delete、删除重建 cursor、字段级 PUT。
