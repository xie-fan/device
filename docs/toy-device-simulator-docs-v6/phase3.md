# Phase 3 详细设计：Web UI 调试台（v6）

复用 Phase 2 API。并发展示：默认 reject，可选 cancel_previous。  
Turn 详情展示 `uplink_end_reason`（含 `vad`）与 `turn_end_reason`，取消后前者不无故变成 `interrupt`。
