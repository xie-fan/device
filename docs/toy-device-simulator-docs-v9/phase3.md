# Phase 3 详细设计：Web UI 调试台（v9）

完全复用 Phase 2 API，无第二套业务逻辑。

## 1. 目标

纯 UI：配置 → 保存 → 启动 → 上传音频 → 看到/听到回复（TTS、指令或文本 JSON）。

## 2. 范围与非目标

**范围：** 设备列表；只读 `device_id` 的配置表单；对话与历史；ACK 指示跟报文标志；skip_register 先夹具再 **新建**；展示 `reply_kind`、`last_error`、`connection_failed`。

**非目标：** 完整产品；主路径麦克风；前端查 DownlinkAck；前端自己实现 wait/stop；把关闭标签做成 interrupt。

## 3. 对 Phase 2 的依赖

- config / turns / frames / audio / events（含 `after_event_seq`）
- speak、speak_and_wait、wait、interrupt、stop、delete
- 失败后 Stopped 可再 start

对话页「发送并等待」用 `speak_and_wait`。轮询/WS 使用事件游标，避免漏事件。关闭页签不自动 interrupt。

并发展示：**默认 reject，可选 cancel_previous**。无 queue UI。

## 4. 页面

1. 设备列表 — 含 Starting/Running/Stopped 与 `last_error`
2. 配置 — `device_id` 只读；format=pcm
3. 对话 — 状态机、ASR、TTS、command、JSON 回复、打断/停止
4. 历史 — `uplink_end_reason`、`turn_end_reason`、`reply_kind`

## 5. 浏览器音频

上传文件为主路径。下行能播则播，否则下载。麦克风 Phase 4。

## 6. 验收

- [ ] 纯 UI 走通配置→对话
- [ ] 不能改 `device_id`
- [ ] 纯 command / JSON 轮次显示已结束，不转圈 20s
- [ ] 断线后可再点启动
- [ ] wait 使用游标或 speak_and_wait，不丢终态
- [ ] ACK 指示跟报文；无前端独有逻辑
