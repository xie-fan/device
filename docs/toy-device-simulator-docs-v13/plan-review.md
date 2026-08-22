# 玩具设备模拟器规划审查（v13）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v13/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v13 设计正文。修订见 `docs/toy-device-simulator-docs-v14/`。

**决策：NEEDS_CHANGES**

阶段边界 **不移动**：P1 单设备 CLI；P2 批量+API+Scenario；P3 UI；P4 按需。

---

## 1. 阻塞问题

### B1. finalizer 重入直接返回，stop/delete 无法等到 Phase C

已 `finalize_started` 则 return，与「调用方等到 Phase C」矛盾。

**改为：** 每 generation 一个共享 `finalize_done`。后来者 **加入同一次收口并等待完成**。并发 `reason` 写死优先级。

### B2. Stage 3 与关队列竞态；取消表自相矛盾

Phase A 置 Disconnecting，规则又规定 Disconnecting 跳过出站，却仍算/发 Stage 3。发送侧只看 Terminal，而 Terminal 在 Phase C，关队列前仍可入队 Stage 1/2。

**改为：** Phase A **原子冻结**该 Turn 生产者。`BeginClose(finalFrame)` 定义队列清理、Stage 3 优先级、异常关闭丢队列。公开 Enqueue 在 Disconnecting 时拒绝；Stage 3 只由 BeginClose 注入。

### B3. 等待预算漏 `post_final_asr_silence`

合法 silent 可能先被 504。

**改为：** `upload + first_reply + max(idle, followup, post_final_asr_silence) + slack`。

### B4. Scenario 示例漏事件

`wait: true` 已等到 Terminal，下一步无游标等 `turn_terminal` 只等未来。

**改为：** 删重复 wait；speak 在 CAS 同锁捕获 `turn_id` 与 `seq_before`；事后 assert 必须带该游标或 `turn_id`。

### B5.（P2）媒体上传不可实现

multipart 仅 `file` 却允许 raw PCM，无采样率/声道，无法算时长。缺 stream 上限、删资产与在读并发、浏览器可播 MIME。

**改为：** HTTP **只接受 WAV**。speak 受理时拷贝 PCM。下载封装 `audio/wav`。

### B6.（P2）WS 游标作用域

`device_id` 未声明必填，但 `event_seq` 是设备级。

**改为：** 缺 `device_id` → **400 不升级**。不做全局事件日志。

### B7.（P2）身份字段可改状态矛盾

Starting/Stopping 改身份会使本 generation 握手与配置分叉。

**改为：** 仅 **Created / Stopped** 可改 enterprise/device_type。`device_id` 永不改。

---

## 2. 非阻塞

- `connection_stopped` 改为「用户主动收口」（含 stop 与 delete 关连接），不要写成仅 `user_stop`。
- v13 的 standalone、正常停机事件、淘汰游标、三段框架、API 覆盖必须保留。

---

## 3. 验证缺口

无实现。修订后验收至少：finalizer 并发加入、关队列竞态、长 `post_final_asr_silence`、Scenario 游标回放、WAV 校验。
