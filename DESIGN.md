# 界面主题设计

## 主题机制

`data-skin` 挂在 `<html>` 上，`localStorage["bench.skin"]` 持久化，与既有明暗主题
（`data-theme` / `bench.theme`）正交。顶栏加"主题"切换组：

- `经典`（默认）— 现有三栏：名册 / 工作区（气泡或轮次卡）/ 事件侧栏
- `调音台` — 通道条 + 灯位桥 + 连续双道时间轴 + 底部事件面板

## 调音台主题

事实约束：多设备但每次只用一台；对话连续多轮；上行/下行可并发；
协议不带 ASR 识别文本；speak 发的是音频不是文字。

布局（CSS grid 重排 `.bench`，DOM 不复制）：

```
┌──────┬──────────────────────────────┐
│ 通道  │ stage__head（启动条，紧凑）    │
│ 条    │ deck__bridge（灯位桥）         │
│ CH1-4 │ deck__tl（连续时间轴）         │
│  ＋   │ dock（sendbar + 播放器）       │
│      ├──────────────────────────────┤
│      │ side（事件/Turn/全局 页签）     │
└──────┴──────────────────────────────┘
```

- **通道条**：`.rail` 收窄成 ~92px 竖列，每台设备一格（LED + device_id 竖排/截断），
  搜索/筛选/批量框在 console 皮肤下隐藏（经典皮肤保留）。
- **灯位桥**：CONN（connection_state）、READY（instance_state=running）、
  UP（本轮上行在流）、DOWN（本轮下行在流）、FAULT（live.fault）。
- **时间轴**：三条道共用一把时间尺——上行（绿段）、下行（紫段）、事件点（菱形，
  红=错误）。turn 是背景色带不带文字；NOW 红线钉右缘；下行静默区画斜纹，
  宽度 = downlink_idle_timeout_sec 预算。段几何来自 framesByTurn 帧日志
  （ts + payload_len + direction），不是假数据。
- **道内无文字**：包数/时长/事件详情全在下方 side 面板。
- **底条**：`● 录制`（MediaRecorder→WAV→选为音源）、`📁 选择文件`（复用
  wav-file 通道）、音源下拉、`送出/打断`。
- 事件点/段的 tooltip（title 属性）携带详情，不占像素。

## 不做

- 不伪造 ASR 文本、不伪造 turn 卡片。
- 管理视图（设备管理/厂商/产品）两皮肤共用，仅变量换色。
- 录制只产 WAV 单声道（浏览器 MediaRecorder→decodeAudioData→PCM16 封装），
  上传复用 `POST /assets`（device_id 触发服务端按设备规格转码）。
