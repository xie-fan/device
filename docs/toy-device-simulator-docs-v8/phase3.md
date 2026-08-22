# Phase 3 详细设计：Web UI 调试台（v8）

为人工提供配置设备与简单对话验证的界面。**完全复用 Phase 2 已交付的 API**，无第二套业务逻辑。

## 1. 目标

纯 UI 完成：配置 → 保存 → 启动 → 上传音频 → 看到/听到回复。

## 2. 范围与非目标

**范围**

- 设备列表（状态、批量启停、从模板创建）
- 配置表单（`GET/PUT /devices/{id}/config`）+ 校验提示；**不可编辑 `device_id`**
- 对话页：上传音频、发送、TTS 播放/下载、ASR、打断、状态可视化、协议日志
- 会话历史（turns / frames / audio）
- ACK 指示：跟报文 `NeedAck` / `need_ack`，不展示「已读 DownlinkAck 配置」
- skip_register：先夹具分配 ID，再 **创建新设备**，不提供「改 ID」控件

**非目标**

- 做成完整产品
- 实时麦克风作为主路径
- 独立于 Phase 2 的后端逻辑
- 在前端查询或假设服务端 `DownlinkAck`
- 前端自行实现 stop/wait 语义（必须调用 Phase 2 API）

## 3. 对 Phase 2 的依赖

Phase 3 启动前必须已有：

- `GET/PUT /devices/{id}/config`（改 `device_id` 返回 400）
- `GET /devices/{id}/turns` 及 `/{turn_id}`、`/frames`、`/audio/uplink`、`/audio/downlink`
- 事件 WebSocket（可按 `device_id` / `turn_id` / `correlation_id` 过滤）
- `speak` / `interrupt` / `speak_and_wait` / `stop` / `delete`
- `POST /wait`（必填 `device_id`）

缺失则不得声称「完全复用 Phase 2 API」。

并发展示文案：**默认 reject，可选 cancel_previous**。不提供 queue UI。

对话页「发送并等待」调用 `speak_and_wait`；用户关闭标签页不得被做成自动 `interrupt`（与 API：断开只取消 waiter 一致）。提供独立「打断」「停止」按钮。

## 4. 主要页面

1. **设备列表页** — 表格、批量启动/停止/删除、从模板创建
2. **设备配置页** — 表单；`format` 固定展示 pcm；`device_id` 只读；`expect_downlink_need_ack` 仅为说明
3. **对话调试页** — 选设备、上传音频、状态机、ASR、TTS 播放或下载、打断、停止、原始协议日志
4. **会话历史页** — 按设备/时间浏览 Turn；展示 `uplink_end_reason`（含 `vad`）与 `turn_end_reason`；取消后前者不无故变成 `interrupt`

生命周期与 report 日志用 `correlation_id`；对话用 `turn_id`。

## 5. 浏览器音频

- 主路径：用户上传已准备好的文件（服务端按 Phase 1/2 解封装为 pcm 上线）
- 下行：浏览器能直接播放则播放，否则提供下载；后端可选转 wav/mp3 仅作为 UI helper
- 实时麦克风：标记为可选，Phase 4

注入相关控件若存在：选项与 fault 矩阵一致；`skip_report` 不得标成「必 drop」；`skip_register` 须先走夹具并创建新实例。

## 6. 验收 Checklist

- [ ] 纯 UI 完成「配置 → 保存 → 启动 → 上传音频 → 看到/听到回复」
- [ ] 配置保存后可被 CLI / API 加载；无法在 UI 修改 `device_id`
- [ ] 实时日志与后端事件一致（`report_echo`、`tts_done`、矩阵内 drop）
- [ ] 连接级事件可见 `correlation_id`；对话事件可见 `turn_id`
- [ ] TTS 可播放或下载
- [ ] 打断后 UI 展示的 `uplink_end_reason` 与后端一致
- [ ] 关闭等待中的请求不会误发 Stage=3
- [ ] ACK 指示跟随报文标志，无「已读 DownlinkAck」
- [ ] 无前端独有业务逻辑
