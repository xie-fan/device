---
target: 台架设备调试 UI
total_score: 33
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 4
timestamp: 2026-09-16T03-11-27Z
slug: toy-device-simulator-ui
---
# 台架 · 设备调试 — 设计评审快照

Method: dual-agent (A: f0868c39 explore · B: c6d6939c detector+manual)

## 总体判断
仪器级调试台，非通用模板。混乱感来源：≈39 个常显控件无优先级分层、自相矛盾的交互规则、对比度/可达性硬伤。

## Nielsen 33/40
1 状态可见 3（wait_ready 30s 无进度、fault 隐身）· 2 匹配 4 · 3 控制 3（busy 不可取消、设备行键盘不可达）· 4 一致性 3（批量删无 arm）· 5 预防 3 · 6 识别 4 · 7 效率 3 · 8 极简 3（stage__acts 10 控件）· 9 恢复 3 · 10 帮助 4

## 检测器
detect.mjs 降级（缺 htmlparser2/css-select/css-tree/domutils，regex 模式 []）。人工扫描：--faint #5c6273 全场 2.7–3.2:1 不达 AA；.dim class→--faint 错配；浅主题 --acc 2.98:1；outline:none 且全文件无 :focus-visible；mast <950px 被 overflow:hidden 裁切。

## 优先问题
- P1 气泡模式「设备回复」应为「服务端回复」（app.js:742/851 方向性错标）
- P1 注入故障后 armed 态全界面无渲染路径
- P1 rail 批量删除无确认（app.js:3442 vs 其他删除全部 armThen 两击）
- P1 .device 行 click-only 无 tabindex/role；#btn-help 无 aria-label（名称为"?"）；按钮无焦点环
- P2 attach-bar 4 下拉与 6 按钮同级混排；wait_ready 30s 盲等
- P2 对比度：--faint 全场 <4.5:1、.dim→--faint、浅主题 acc 链接 2.98:1
- P3 ⌘⏎ 在 Windows 显示 Mac 键符（index.html:176，实际 Ctrl+Enter 可用）；mast <950px 裁切；openDrawer("new") 传送视图（app.js:2324）；tape-count 与可见行不符；「关键」chip 实为「隐藏包突发行」

## Persona
协议工程师：无单条事件 JSON 展开、包突发行伪造类型名、快捷键稀少。QA：术语墙+三个陷阱（批量删、故障隐身、视图劫持）、⌘⏎ 错标。

## 优势
等待时刻仪表化（idleNoteFor）、armThen+lockbar 护栏、幂等渲染（签名比对+尾部增量）。
