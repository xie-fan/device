# Phase 3 详细设计：Web UI 调试台（v3）

## 1. 目标

为人工提供配置设备与简单对话验证的界面。**完全复用 Phase 2 已定义并交付的 API**，无第二套业务逻辑。

## 2. 范围与非目标

**范围**

- 设备列表（状态、批量启停）
- 配置表单（读写走 Phase 2 config API）+ 校验提示
- 对话页：上传音频、发送、TTS 播放/下载、ASR、打断、状态可视化、协议日志
- 会话历史（turns / frames / audio）
- ACK 状态展示（下行出现 `NeedAck=1` 时）

**非目标**

- 做成完整产品
- 实时麦克风作为主路径
- 独立于 Phase 2 的后端逻辑

## 3. 对 Phase 2 的依赖

Phase 3 启动前必须已有：

- `GET/PUT /devices/{id}/config`
- `GET /devices/{id}/turns` 及 `/{turn_id}`、`/frames`、`/audio/uplink`、`/audio/downlink`
- 事件 WebSocket（可按 `device_id` / `turn_id` 过滤）
- speak / interrupt / speak_and_wait
- `POST /wait`（带 `device_id`）

缺失则不得声称「完全复用 Phase 2 API」。

## 4. 主要页面

1. **设备列表页** — 表格、批量操作、从模板创建
2. **设备配置页** — 表单、保存、校验提示（`expect_downlink_need_ack` 仅为机型预期说明）
3. **对话调试页** — 上传音频、状态机、ASR、TTS 播放/下载、打断、协议日志
4. **会话历史页** — 按设备/时间浏览 Turn、帧日志与音频

## 5. 浏览器音频

- 主路径：上传已准备好的文件
- 下行：浏览器能播则播，否则下载，或后端可选转 wav/mp3
- 实时麦克风：可选后置

## 6. 验收 Checklist

- [ ] 纯 UI 完成「配置 → 保存 → 启动 → 上传音频 → 看到/听到回复」
- [ ] 配置保存后可被 CLI / API 加载
- [ ] 实时日志与后端事件一致（含 `tts_done` / `expected_server_drop`）
- [ ] TTS 可播放或下载
- [ ] 打断生效（先 Stage=3 再换 UUID）
- [ ] 无前端独有业务逻辑
