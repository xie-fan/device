# Phase 3 详细设计：Web UI 调试台（v12）

阶段边界不变：只消费 Phase 2 API，无第二套后端。

## 1. 目标

配置 → 保存 → 启动 → 上传音频 → 看到/听到回复。

## 2. 范围

列表（instance/connection、last_activity）；device_id 只读；playingMode 热更；对话；历史。槽占用/释放跟 `turn_terminal`，不要只等 `tts_done`。事件用 WS `after_event_seq` 续传，410 则从 `oldest_seq` 重建。

**非目标：** 完整产品；主路径麦克风；前端查 DownlinkAck。

## 3. 依赖

Phase 2 §5 的 speak/wait/WS/错误码。关闭标签不等于 interrupt。

## 4. 验收

- [ ] 纯 UI 走通主路径
- [ ] command/JSON/silent 轮次结束时 UI 释放「占用中」（收到 `turn_terminal`）
- [ ] 不能改 device_id；playingMode 可热更
- [ ] 断线后可再启动；无前端独有逻辑
