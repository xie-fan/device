# Phase 3 详细设计：Web UI 调试台（v17）

阶段边界不变：只消费 Phase 2 API，无第二套后端、无其它版本依赖。

## 1. 目标

配置 → 保存 → 启动 → wait_ready（body 带 instance_id 与 conn_generation）→ 上传 WAV → 看到/听到回复。

## 2. 范围

列表：device_id、instance_id、instance_state、connection_state、last_activity、last_error。  
Starting 时禁用说话。WS 查询串必须带 device_id 与 instance_id。TTL 内删除设备的历史用同一 instance_id 拉 tombstone，不要换到新实例的 seq。播放 GET `.../audio/downlink`（audio/wav）。槽释放看 turn_terminal。playingMode 走 POST report。身份/音频仅 Created/Stopped 可改。用户 stop 不是故障。

**非目标：** 麦克风；查 DownlinkAck；关闭标签=interrupt。

## 3. 验收

纯 UI 走通上传到听到回复；command/JSON/silent 靠 turn_terminal 释放占用；断线后再 start 用新 conn_generation 再 wait_ready；Running 改采样率 409；无前端独有业务逻辑。
