# Phase 3 详细设计：Web UI 调试台（v10）

完全复用 Phase 2 API。

## 1. 目标

配置 → 保存 → 启动 → 上传音频 → 看到 TTS / 指令 / 文本 JSON。支持从模板批量创建与错峰启动。

## 2. 范围

设备列表（含 last_error、Starting 不会永久转圈）；`device_id` 只读；对话与历史（`reply_kind`）；ACK 跟报文；模板批量；skip_register 夹具新建。

**非目标：** 完整产品；主路径麦克风；前端查 DownlinkAck；关闭标签=interrupt。

## 3. 依赖

Phase 2 单台+批量 API、events 游标、speak_and_wait、stop、失败后可再 start。

批量创建走 `POST /devices`+template；批量启动走 `batch/start`+stagger。对话「发送并等待」用 speak_and_wait。

## 4. 页面

1. 列表 — 批量启停、模板创建、max_connections 满时提示 429
2. 配置 — device_id 只读
3. 对话 — ASR IsFinal 与 interim 区分；长音频时 command 不显示「已结束」直到 WaitingReply
4. 历史 — uplink/turn_end_reason、reply_kind

## 5. 验收

- [ ] 纯 UI 走通单台对话
- [ ] 模板批量创建与错峰启动可见
- [ ] 不能改 device_id
- [ ] register 无响应后 Stopped 可再点启动
- [ ] 纯 command 不空转 20s；interim ASR 不显示成功
- [ ] 无前端独有业务逻辑
