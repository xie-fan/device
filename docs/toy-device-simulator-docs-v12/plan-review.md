# 玩具设备模拟器规划审查（v12）

审查日期：2026-08-22  
审查范围：`docs/toy-device-simulator-docs-v12/`  
基线：`5a02d70cdf964bdafea7be92495ad1d0a63499c5`  
本文不改 v12 设计正文。修订见 `docs/toy-device-simulator-docs-v13/`。

**决策：NEEDS_CHANGES**

阶段边界 **不移动**：P1 单设备 CLI；P2 批量+API+Scenario；P3 UI；P4 按需。

---

## 1. 阻塞问题

### B1. connection_finalizer 锁与清理顺序

ACK/timeout 持 `conn_mu` 调用 finalizer；finalizer 再持未定义的锁做 Turn 终态、drain、关 socket → 重入或锁序反转。permit 在关 socket **之前**释放，drain 的 2s 内可再 start，突破 `max_connections`。未写 `Connection=Disconnected`。

**改为：** 三段：锁内声明 finalizing → 锁外 drain/close → 锁内提交 Stopped、释放 permit、写事件。register 路径必须先解锁再 `request_finalize`。permit 只在 socket 已关之后释放。

### B2. 正常 stop/delete 记成 connection_failed

finalizer 无条件 append `connection_failed`，污染告警与 UI。

**改为：** `user_stop` → `connection_stopped`；`user_delete` → `device_deleted`（若仍在连则先正常收口）；异常 reason 才 `connection_failed` 并写 `last_error`。

### B3. event cursor off-by-one

`after_event_seq < oldest_seq` 会使 `after_event_seq=0` 在 `oldest_seq=1` 时 410。

**改为：** 维护 `evicted_through_seq`；过期当且仅当 `after_event_seq < evicted_through_seq`。允许 `after_event_seq == oldest_seq - 1`。

### B4.（P2）standalone 被「同前一版」破坏

完成矩阵、取消表、fault 帧、计时器启动/重置未写全；Phase 1 范围含完成矩阵但正文没有。

**改为：** v13 目录内写完整契约，禁止指向其它版本。

### B5.（P2）speak 媒体无法支撑 UI

REST 收服务器本地路径：浏览器不能上传，且任意读文件。无下行音频下载，UI 不能播放。

**改为：** multipart / `asset_id`；限制根目录、大小、时长。补录音下载 API。CLI `--audio` 仍仅 Phase 1 本地路径。

### B6.（P2）API 范围回退

缺设备列表、配置、frames/audio；batch stop/delete 消失；templates/scenarios 无 body；batch 部分成功状态未定。

**改为：** 在 Phase 2 写全请求/响应（不把 UI 提前）。

---

## 2. 非阻塞

- `write_queue_depth` / drain：Phase 1 属设备 YAML；Phase 2 **只**属 Manager，设备 YAML 禁止这两项。
- v12 已修好 `/wait` 窗口、register 一次性消费、`turn_terminal`。须保留。

---

## 3. 验证缺口

无实现。下一版验收至少：finalizer 重入、permit 在 close 之后、正常 stop 事件、游标 0 / oldest-1 / oldest-2、上传与播放闭环。
