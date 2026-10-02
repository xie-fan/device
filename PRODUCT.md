# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

单一用户：本项目作者本人，一名在 Windows 上做玩具设备协议联调的工程师。使用场景是本地调试台——对着自研后端（ai-creates-wealth）验证模拟设备的 WebSocket 协议行为。偶尔也给 QA 同事用。（推断：仓库为个人工具，无多用户/权限设计）

## Product Purpose

toy-device-simulator 的管理台（manager UI）。驱动一批模拟设备实例走完 Wi-Fi + WebSocket 聊天协议全生命周期：注册、上报、音频上行、TTS 下行、turn 收尾、故障注入、帧录制。成功 = 用户能在不读代码的情况下确认"设备在该协议下行为正确"，并能把异常精确定位到某帧某事件。

## Positioning

协议级调试仪器，不是通用设备管理后台。差异化机制：事件流、帧日志、turn 状态机、故障注入全部按真实协议语义建模（wire 值、stage、uuid、静默收尾计时），UI 直接讲协议自己的语言。

## Operating Context

- 本机 loopback（127.0.0.1:8090），与 echosrv 或真实后端联调
- 典型会话：选设备 → 挂靠环境/产品 → 启动 → speak → 盯事件带与轮次卡片 → 看 frames.jsonl/录音
- 长时间开着盯事件流；暗色使用环境推断成立（现有主题默认暗色，且为调试工具惯例）

## Capabilities and Constraints

- 前端为 Go embed 的静态三件套（index.html / app.css / app.js），无构建管线、无框架——改 CSS 后必须重编 manager
- 双主题（暗/浅）已有；三栏布局（名册 / 对话区 / 事件带）；抽屉式二级面板
- 术语必须与协议一致（服务端回复、上行/下行、turn、stage、ready 等），不得发明"用户友好"改名
- 信息密度是功能不是缺陷：事件带、轮次卡片的状态字段一个不能丢，美化只能在呈现层做

## Brand Commitments

无品牌资产。已有语义色约定须保留：事件/状态色（ok=绿、warn=黄、err=红、acc=紫蓝）在深浅两主题下已校准对比度。

## Evidence on Hand

- 既有实现即事实源：ui/app.css 全部 token、app.js 渲染逻辑、index.html 结构
- 协议事实：docs/toy-device-websocket-protocol.md、CONTEXT.md
- 上一轮 critique 快照：.impeccable/critique/（33/40，P1 已修）

## Product Principles

- 状态可见性优先于美观：等待、故障、覆盖、终态必须一眼可辨
- 说协议自己的语言：wire 值与端点名即标签
- 密度服务于扫读：列表与卡片为"扫一眼定位异常"优化，不为留白而留白
- 幂等渲染纪律：高频事件流不得冲掉用户正在操作的控件
