# Phase 3 详细设计：Web UI 调试台（v4）

完全复用 Phase 2 API，无第二套业务逻辑。

## 范围

设备列表、配置表单、对话页、会话历史、ACK 展示（`NeedAck=1` 时）。

非目标：完整产品、麦克风主路径、独立后端。

## Phase 2 依赖

`GET/PUT config`；turns / frames / audio；带 `device_id` 的事件流与 `POST /wait`；speak / interrupt / speak_and_wait。  
注入相关 UI 若存在，选项与矩阵一致：`skip_report` 不得展示为「必 drop」。

## 页面

1. 设备列表
2. 配置（含 `channels` / `sample_format` 只读或固定 mono s16le）
3. 对话调试
4. 会话历史

下行：能播则播，否则下载。

## 验收

- [ ] UI 闭环：配置 → 保存 → 启动 → 上传音频 → 看到/听到回复
- [ ] 配置可被 CLI/API 加载
- [ ] 日志与事件一致（`report_echo` / `tts_done` / 矩阵内 drop）
- [ ] 打断不把已完成上行改成 `uplink_end_reason=interrupt`（展示后端字段即可）
- [ ] 无前端独有业务逻辑
