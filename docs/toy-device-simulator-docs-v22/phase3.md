# Phase 3 详细设计：Web UI 调试台（v22）

阶段边界不变：只消费 Phase 2 API，无第二套后端、无其它版本依赖。

## 1. 目标

配置 → 保存 → 启动 → wait_ready（body 带 instance_id 与 conn_generation）→ 上传 WAV → 看到/听到回复。

## 2. 范围

列表：device_id、instance_id、instance_state、connection_state、last_activity、last_error。  
Starting 时禁用说话。WS 查询串必须带 device_id 与 instance_id。省略 `after_event_seq` 会从 oldest 回放（先写完 backlog 切片，再写积压，inbox 空了才进入 256 上限）。若只要未来，传入当前 newest。删除设备时 WS 应收到 `device_deleted` 再关（drain）；客户端停读或半开按 abort，空闲探测从进入空闲起不超过 `write_drain_timeout_sec`。live 事件按 `event_seq` 升序；UI 不得再轮询补 `turn_terminal`。TTL 内删除设备的历史用同一 instance_id 拉 tombstone（回放到 `device_deleted` 后 WS 关闭），不要换到新实例的 seq。播放 `GET .../audio/downlink?instance_id=`（audio/wav）。Turn 列表同样带 instance_id。槽释放看 turn_terminal。UI 打断按钮调 `POST /interrupt`（body 带 live instance_id），不断开连接；随后 stop 仍应能看到已入队的 Stage=3 被写出。playingMode 走 POST report。身份/音频仅 Created/Stopped 可改。用户 stop 不是故障。

**非目标：** 麦克风；查 DownlinkAck；关闭标签=interrupt。

## 3. 验收

纯 UI 走通上传到听到回复；command/JSON/silent 靠 turn_terminal 释放占用；打断后仍保持连接并可再说话；断线后再 start 用新 conn_generation 再 wait_ready；Running 改采样率 409；无前端独有业务逻辑。
