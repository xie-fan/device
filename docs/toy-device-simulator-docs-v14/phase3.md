# Phase 3 详细设计：Web UI 调试台（v14）

阶段边界不变：完全复用 Phase 2 API，无第二套后端。

## 1. 目标

配置 → 保存 → 启动 → **上传 WAV** → 看到/听到回复。支持模板批量与错峰启动。

## 2. 范围

设备列表（`instance_state` / `connection_state` / `last_activity` / `last_error`）；`device_id` 只读；playingMode 可在 Ready 后经 report 热更；对话与历史。

上传：`POST /assets`（multipart WAV），speak 只传 `asset_id`。播放：`GET .../audio/downlink`（`audio/wav`）供 `<audio src>`。槽释放跟 `turn_terminal`，不要只等 `tts_done`。事件 WS **必须带 `device_id`**；用 `after_event_seq` 续传；410 则用 `evicted_through_seq` 重建。

正常 stop 显示为停止（`connection_stopped`），不要当故障告警。`last_error` 仅异常收口展示。

**身份字段（enterprise / device_type）：** 仅 Created/Stopped 可改（与 Phase 2 一致）。Starting/Stopping 禁用保存。

**非目标：** 完整产品；主路径麦克风；前端查 DownlinkAck；关闭标签=interrupt；把文件路径或 raw PCM 发给 REST。

## 3. 依赖

Phase 2 §6 全表，含 WAV 资产、`audio/wav`、batch stop/delete、207、WS 必填 `device_id`。

## 4. 页面

1. 列表 — 批量启停、模板创建、限额 429、部分成功 207
2. 配置 — device_id 只读；身份字段仅停止后（Stopped）可改
3. 对话 — 上传 WAV；TTS/command/JSON；`<audio>` 播放 downlink；keepalive 用 last_activity
4. 历史 — frames 与 uplink/downlink WAV 下载；`uplink_end_reason` / `turn_end_reason` / `reply_kind`

## 5. 验收

- [ ] 纯 UI 走通：上传 WAV → 对话 → **听到**回复（`<audio>` + `audio/wav`）
- [ ] command/JSON/silent 轮次结束时释放「占用中」（`turn_terminal`）
- [ ] 不能改 device_id；身份字段在 Starting/Stopping/Running 保存失败（409）；Stopped 后可改
- [ ] playingMode 可在 Ready 热更
- [ ] 用户停止不显示为连接失败
- [ ] 断线后可再启动；Starting/Stopping 不会永久转圈（stop 等到 Stopped）
- [ ] 无前端独有业务逻辑
