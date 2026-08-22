# Phase 3 详细设计：Web UI 调试台（v16）

阶段边界不变：只消费 Phase 2。不增加前端独有 API。

## 1–2. 目标与范围

配置→保存→启动→**wait_ready（带返回的 instance_id 与 conn_generation）**→上传 WAV→听到 `audio/wav`。

列表：`instance_id`、状态。WS **必须** `device_id`+`instance_id`。删除重建用新 instance，禁止沿用旧游标。配置 allowlist 同 Phase 2。playingMode 走 report。

## 3. 验收

Starting 禁用说话；speakable 后可播；`turn_terminal` 释放占用；断线再 start 用新 generation 再 wait_ready；Running 改采样率 409；无前端私有逻辑。
