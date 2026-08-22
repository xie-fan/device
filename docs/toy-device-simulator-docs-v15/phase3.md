# Phase 3 详细设计：Web UI 调试台（v15）

阶段边界不变：只消费 Phase 2，无第二套后端。

## 1. 目标

配置 → 保存 → 启动 → **等可说话** → 上传 WAV → 看到/听到回复。

## 2. 范围

列表展示 `instance_id` / `instance_state` / `connection_state`。启动后须等 speakable（`wait_ready` 或事件 `ready`/`connected`/`registered` 按路径）再允许点「说话」。上传 WAV；播放 `audio/wav`。槽释放跟 `turn_terminal`。WS 必带 `device_id`，建议带 `instance_id`。删除后列表消失；重建是新 instance，历史走 tombstone/`instance_id`，不要把旧游标接到新实例。

配置：allowlist 与 Phase 2 一致。身份/音频仅 Stopped 或 Created 可改。playingMode 在 Ready 走 report，不要 PUT。

用户 stop 显示停止。Created 设备 stop 也只是停在 Stopped，不是故障。

**非目标：** 麦克风；查 DownlinkAck；关闭标签=interrupt。

## 3. 验收

- [ ] 启动按钮在 Starting 时禁用说话；speakable 后可上传播放
- [ ] 听到 downlink WAV；command/JSON/silent 靠 `turn_terminal` 释放占用
- [ ] 断线后可再启动（新 generation）
- [ ] Running 改采样率/机型保存 → 409
- [ ] 删除再创建同 ID：对话页不串上一台的事件
- [ ] 无前端独有业务逻辑
