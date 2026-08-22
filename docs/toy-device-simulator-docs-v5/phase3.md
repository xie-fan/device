# Phase 3 详细设计：Web UI 调试台（v5）

复用 Phase 2 API。

依赖：config、turns/frames/audio、带 device_id 的事件与 wait、speak/interrupt。  
并发策略展示为：默认 reject，可选 cancel_previous。

页面：设备列表、配置（`format` 固定 pcm）、对话、历史。

验收：UI 闭环；日志含 `report_echo`（带 sequence_number）；打断展示后端原因字段；无前端业务逻辑。
