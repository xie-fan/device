# Phase 3 详细设计：Web UI 调试台（v11）

完全复用 Phase 2 API。产品目标：人工配置 + 简单对话，而不是第二套后端。

## 1. 目标

配置 → 保存 → 启动 → 上传音频 → 看到/听到回复。支持模板批量与错峰启动。

## 2. 范围

设备列表（`instance_state` / `connection_state` / `last_activity` / `last_error`）；`device_id` 只读；playingMode 可在 Ready 后热更；对话与历史；ACK 跟报文标志。

**非目标：** 完整产品；主路径麦克风；前端查 DownlinkAck；关闭标签=interrupt。

## 3. 依赖

Phase 2 单台+批量、speak_and_wait、events 游标、失败后 Stopped 可再 start、report 热更 playingMode。

## 4. 页面

1. 列表 — 批量启停、模板创建、限额 429 提示
2. 配置 — device_id 只读；身份字段需停止后改
3. 对话 — TTS / command / JSON；keepalive 不掉线可从 last_activity 观察
4. 历史 — uplink/turn_end_reason、reply_kind

## 5. 验收

- [ ] 纯 UI 走通单台主路径（握手到听到回复）
- [ ] 模板批量创建与错峰启动
- [ ] 不能改 device_id；playingMode 可热更
- [ ] 断线后可再启动；Starting 不会永久转圈
- [ ] 无前端独有业务逻辑
