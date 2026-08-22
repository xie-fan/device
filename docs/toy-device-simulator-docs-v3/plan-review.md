# 玩具设备模拟器规划审查（v3）

审查日期：2026-08-21  
审查范围：`docs/toy-device-simulator-docs-v3/`  
对照依据：v3 正文；基线 `C:\Users\xie_f\projects\other\ai-creates-wealth` @ `5a02d70cdf964bdafea7be92495ad1d0a63499c5`（`support.go` `LookupCachedDevicePlayMode`、`mqtt/service/client.go` `ParseAudioHeader`、`websocket/controller/report.go`）；`docs/toy-device-websocket-protocol.md` §5.2  
审查对象：v3 实施计划。本文不改 v3 正文。

**决策：NEEDS_CHANGES**

v3 已正确关闭基线 commit、raw injection、`tts_done`、wait 作用域、Stage=4 和 ACK 配置歧义，但「规划层 P1 已关闭」仍说早了。

---

## 1. 结论摘要

| 类别 | 数量 |
|------|------|
| 阻塞 | 4 |
| 非阻塞 | 2 |
| 验证缺口 | 见第 5 节 |

v3 已关且可保留：对齐 commit、注入帧必须出站、`tts_done` 定义、`POST /wait` 必填 `device_id`、Speaking+Stage=4、`NeedAck=1` 必须 ACK。

不可按原文执行：所有注入统一期待 `expected_server_drop`；把 report 回显当 ACK；`cancel_previous` 一律改写 `uplink_end_reason=interrupt`；silence 只写格式/采样率。

---

## 2. 阻塞问题

### B1. 所有注入统一期待 drop，结果不成立

**位置：** `phase1.md` §10 注入验收；`phase2.md` §8

`skip_register|skip_report|bad_seq|oversize|bad_header` 被写成全部产生 `expected_server_drop`。对照基线：

| fault | 基线事实 | 统一 drop 为何错 |
|-------|----------|------------------|
| `skip_register` | `LookupCachedDevicePlayMode` 只看内存缓存或库里 `status=1`，不要求本连接执行过 register | 已有 `status=1` 设备跳过 register 仍可通过校验并发音 |
| `skip_report` | 音频入口用缓存/库里的 `playing_mode` | 历史模式可能非 0，不保证丢弃 |
| `bad_header`「padding 错误」 | `ParseAudioHeader` 只检查 `len >= 100` 再反序列化，不校验 padding/magic | 100 字节坏 padding 仍能解析 |
| Phase 2 `dup_uuid` / 任意错误 Stage | 基线无「必丢」依据 | 不能当 drop 验收 |

**必须改为逐 fault 矩阵：** 前置数据、精确出站字节、期望事件。  
`skip_register` 必须使用确认不存在的新 ID；`skip_report` 不默认期待 drop；`bad_header` 固定为剥掉 `'0'` 后 **<100 字节** 等确定会 `ParseAudioHeader` 失败的样本。

### B2. report 回显仍被误写成 ACK

**位置：** `architecture.md` §4.2 事件表（`ack_failure` 含 report）；`phase1.md` Connection：Reporting → Ready

基线 `report.go` 下行是 `SendDeviceMsg(..., request)`，即原始 `ReportData` 回显。协议 §5.2：`/report/client` 的 `data` 不是 `AckResponse`。

Reporting → Ready 的条件是匹配到 topic 以 `/report/client` 结尾的管理帧且 `data` 为回显的 `ReportData`，不能等待或解析不存在的 register 式 ACK。`ack_failure` 仅用于 **register** ACK `code != 0`（含 5001）。

### B3. `cancel_previous` 会篡改已完成的上行事实

**位置：** `phase2.md` §4

当前写死 `uplink_end_reason=interrupt`。若本 Turn 已发 Stage=2、处于 WaitingReply / PlayingTTS，真实上行收口已是 `stage2`，只能把 `turn_end_reason` 改为 `interrupt`。

还需明确：**active turn 持续到整个 Turn terminal**（`turn_end_reason` 已赋值），不是上传一结束就不算 active。否则 WaitingReply 期间第二次 `speak` 会以为没有 active turn。

### B4. 「精确 silence」仍无法唯一实现

**位置：** `phase2.md` §7；`phase1.md` 音频配置

只有 format / sample_rate，没有声道数、位深。多个 WAV 直接按文件拼接会重复 RIFF 头。

必须规定统一内部 PCM（例如 **mono、signed 16-bit LE**），所有 WAV **解封装后再进入同一时间轴**；silence 在该 PCM 上生成全零采样，再编码到线上格式。

---

## 3. 非阻塞说明

- `POST /devices/{id}/faults` 只能在相关生命周期步骤前配置，或触发断线重建；已 Ready 再设 `skip_register` 无意义，应拒绝。
- `queue` 仍缺最大长度、取消和超时。Phase 2 可先只交付默认 `reject`；`cancel_previous` 按 B3 修对后保留。

---

## 4. 建议改入规划的条款（已写入 v4）

1. 注入验收改为 **fault 矩阵**，禁止「凡注入必 drop」。
2. `ack_failure` 仅 register；Ready 等 `/report/client` 回显。
3. `cancel_previous`：已 Stage=2 则保留 `uplink_end_reason=stage2`，只改 `turn_end_reason`；active 直到 Turn 结束。
4. 内部 PCM：mono s16le；WAV 先解封装；配置写明 `channels` / `sample_format`。

---

## 5. 验证缺口

- 已确认基线 commit `5a02d70cdf964bdafea7be92495ad1d0a63499c5` 存在，提交信息匹配；相关服务端文件相对该 SHA 无差异。
- 尚未运行真实 core TTS 冒烟，也没有 golden/audio 实物。
- 本审查针对 v3 文件；修订正文见 `docs/toy-device-simulator-docs-v4/`。
