# Phase 3 详细设计：Web UI 调试台（审查后修订版）

## 1. 目标

为人工提供配置设备与简单对话验证的界面。**完全复用 Phase 2 已定义并交付的 API**，无第二套业务逻辑。

## 2. 范围与非目标

**范围**

- 设备列表（状态、批量启停）
- 配置表单（读写均走 Phase 2 的 config API）+ 校验提示
- 对话页：上传音频、发送、TTS 播放/下载、ASR 展示、打断、状态可视化、实时协议日志
- 会话历史浏览（走 Phase 2 的 turns / frames / audio 查询接口）
- ACK 状态展示（如启用）

**非目标**

- 把 UI 做成完整产品
- 复杂实时麦克风编解码作为主路径（可选后置）
- 独立于 Phase 2 API 的后端逻辑

## 3. 与 Phase 2 的依赖（必须先满足）

Phase 3 启动前，Phase 2 必须已交付以下接口：

- `GET/PUT /devices/{id}/config`
- `GET /devices/{id}/turns`
- `GET /devices/{id}/turns/{turn_id}`
- `GET /devices/{id}/turns/{turn_id}/frames`
- `GET /devices/{id}/turns/{turn_id}/audio/uplink`
- `GET /devices/{id}/turns/{turn_id}/audio/downlink`
- 事件 WebSocket 订阅
- speak / interrupt / speak_and_wait 等操作接口

若上述接口缺失，不得声称「完全复用 Phase 2 API」。

## 4. 主要页面

1. **设备列表页** — 表格、批量操作、从模板创建
2. **设备配置页** — 表单编辑、保存（PUT config）、校验提示
3. **对话调试页** — 上传音频、状态可视化、ASR、TTS 播放/下载、打断、协议日志流
4. **会话历史页** — 按设备/时间浏览 Turn、查看帧日志与音频

## 5. 浏览器音频策略

- 主路径：用户上传已准备好的音频文件
- 下行：优先播放浏览器能直接支持的格式；否则提供下载，或由后端可选转成 wav/mp3
- 实时麦克风：标记为可选增强

## 6. 验收 Checklist

- [ ] 纯 UI 完成「配置设备 → 保存 → 启动 → 上传音频 → 看到/听到回复」闭环
- [ ] 配置保存后可被 CLI / API 正确加载
- [ ] 实时日志与状态与后端事件一致
- [ ] TTS 可播放或下载
- [ ] 打断生效
- [ ] 无前端独有业务逻辑，全部走 Phase 2 API
