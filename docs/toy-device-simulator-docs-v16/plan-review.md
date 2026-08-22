# 玩具设备模拟器规划审查（v16）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v16/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v16 设计正文。修订见 `docs/toy-device-simulator-docs-v17/`。

**决策：NEEDS_CHANGES**

迭代原则不变：只补契约与正确性；不为阶段加产品功能。  
**文风：** 每个版本目录必须写全正文，禁止引用其它版本目录。同目录文件可以互相参照，但被参照的表必须在本目录写全。

---

## 1. 阻塞

### B1. tombstone 路由自相矛盾

算法是「live 不匹配再查 tombstone」，同时又禁止该回退；验收还要求旧实例 404。

**唯一答案（TTL 内）：** 用 `instance_id` 精确命中 live **或** tombstone。命中 tombstone → **200 回放历史**（含 `device_deleted`）。未命中或 TTL 过期 → **404**。禁止把旧 `after_event_seq` 接到新 live。禁止再写互相否定的两句。

### B2. 持锁读完全文件则拷贝中 DELETE 无法改 epoch

**改为：** 短锁读 `path+epoch` → 无锁读盘 → 再短锁复验 epoch。DELETE 只短锁 `epoch++` 并 unlink。验收改为该窗口内 epoch 变化 → 404。

### B3. wait_ready 失败不唤醒

**改为：** Phase C 在同一临界区置 `finalize_committed=true`，取出该 `(instance_id, conn_generation)` 的 speakable waiter，解锁后 **409 `generation_gone` 唤醒**。登记时若已 committed 立即 409。不得拖到 504。

### B4. BeginClose 未处理已排队帧

**改为：** 优雅关闭：丢弃 outbound buffer 中本 Turn 未写出的 Stage 1/2（未发的 Stage 2 不得发出），再把 Stage 3 作为本连接最后一帧。异常关闭：清空 buffer，无 Stage 3。

### B5. API 仍缺方法路径与 stream 绑定

写明 `POST /devices/{id}/speak`、`POST /scenarios/run`。stream 条数/总时长/silence/WAV 格式对齐 Manager 限额与设备 `audio_*`。

### B6. 完成矩阵被压成只有计时器

恢复终止表：每条路径的 `reply_kind`、`turn_end_reason`、是否 `tts_done` / `expected_server_drop`。Phase 1 正文重复该表。

---

## 2. 非阻塞

阶段边界未破。token 方向正确，但队列清理未写。冒烟须在声明的基线提交上做，不要混用户未提交改动。

---

## 3. 验证缺口

无实现、无自动化测试。
