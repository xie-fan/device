# Phase 6 多格式音频的本地闭环

Phase 5 把 `pcm/wav/mp3/amr/aac` 的**上行**打穿，并在真实服务端实测过 mp3 与 amr-wb。
本阶段**不加新格式**，只补三处缺口，让「网页导入任意格式 → 设备对话正确用上 → 上下行都是该格式」
在**本地可重复验收**，而不再依赖一次性的人工实测。

## 0. 起点复核（2026-09-03 实测）

四台设备（wav/mp3/amr/aac）交叉喂四种格式的资产（wav←mp3、mp3←amr、amr←aac、aac←wav），
四条全部 `turn_terminal / reply_kind=tts / turn_end_reason=idle / uplink_end_reason=stage2`，
上行落盘字节格式正确（`RIFF` / `ID3`+MPEG / `#!AMR-WB` / ADTS `FF F1`），
八条回放接口全 200（默认解码 wav、`?raw=1` 给对应 Content-Type）。
wav 设备整段只有一个 RIFF 头（88748 B = 44 + 2772ms × 32000 B/s），逐片封装的老 bug 未复发。

**结论：上行侧除 mp3 的 ID3 头外无缺口。** 本阶段其余两项在下行侧与库侧。

## 1. echosrv 按设备上行格式回 TTS（含分包拟真）

改前：`streamTTS` 写死 `NewPCMHeader` + 440Hz 正弦裸 PCM，不看设备格式，
于是任何设备的 `down_format` 都是 `pcm`——**下行的格式感知在本地零覆盖**。
真实服务端是 `audioConfig.Format = c.Format`（下行格式跟随上行，见 §4 对照）。

改后：

- 上行首帧的 `AudioFormat`/`SamplingRate` 决定下行格式与采样率；正弦 PCM 经
  `media.TranscodeFile` 转到该格式后分包下发，帧头 `AudioFormat` 写实际格式。
- **无 ffmpeg、编码器缺失或转码失败 → 退回 PCM**（日志一行说明），Phase 1–5 的既有本地验收不回退。
- **分包形态照抄服务端**（服务端注释写明这些切法是迁就固件、不按编码规范）：
  | 格式 | 切包规则 |
  |---|---|
  | amr | 每包独立「文件式」：包首带 `#!AMR-WB\n`（或 NB）存储头 + 整数个 AMR 帧 |
  | aac | 20 KB 字节硬切，首尾可为半 ADTS 帧 |
  | mp3 | 帧对齐切分（不切断 MPEG 帧） |
  | pcm / wav | 按字节等分（无帧结构，维持原行为） |
- 推流节奏按各包**字节占比**分摊总时长，不再固定 100 ms 一帧——20 KB 的 AAC 包本就
  对应一秒多音频，固定节奏会失真。**包间隔封顶 1 s**：实测 20 s 的 AMR 下行按字节占比
  算出约 7 s 的包间隔，超过设备的 `downlink_idle_timeout_sec`，turn 在第一包后就 idle
  收尾、后续包全丢（只落了 20444 B）。封顶后 61027 B、3 包全部落地。

**wav 设备的下行维持回 pcm**，这与真实服务端一致：下行格式契约注册表里确实没有 `wav`
（服务端自身测试 `TestBuiltinAudioFormatContractsRejectInvalidProfiles` 断言 wav profile 必须
Resolve 失败），但 wav 在下行侧被归入 PCM 家族处理——**实测 wav 设备照常收到 TTS，
`down_format=pcm`**（见 §6）。所以 echosrv 回 pcm 不是妥协，就是拟真。

**落盘契约不变**：下行字节仍原样追加进 `downlink.pcm`。AMR 每包带头意味着该文件里会有
多个 `#!AMR-WB` 头——这是真实服务端本来就有的形态，**修在回放侧**：
`GET .../audio/downlink` 解码前只保留首个存储头（`stripRepeatedAMRHeaders`），
`?raw=1` 与落盘字节一律不动。

实测（20 s AMR-WB 下行、61027 B、文件内 3 个存储头）：去重前 ffmpeg 把中间的头当成
mode 4 坏帧吃掉、读成 **20.684 s**；去重后 **20.000 s**，`?raw=1` 仍与落盘逐字节一致。

## 2. 音频库解码试听

改前：`GET /assets/{id}/content` 只吐原始字节，UI 因而只对 wav/mp3/aac 开试听、amr 灰掉。
可同一个 manager 在 `audio/uplink` 那条路上早就在用 ffmpeg 解码成 wav 返回——能力现成、没接到库上。
导进去听不了，就无法确认导对了。

改后：`GET /assets/{id}/content?decode=1` 返回解码后的 `audio/wav`（复用 `transcodeBytesToWAV`）。

- 不带参数 = 原始字节（现状不变，`?raw=1` 语义不引入，库这边只有「原始/解码」两态）。
- wav 资产直接原样返回；无 ffmpeg 时压缩格式 400 并提示去掉 `decode`。
- 保留 `http.ServeContent` 的 Range 支持（浏览器媒体栈探测时长要 seek 文件尾）。
- UI 删掉 `LIB_PLAYABLE` 白名单，全格式统一走 `?decode=1` 试听；**下载按钮仍指向原始字节**
  （`playHref` 的 `dlHref`），试听给解码结果、下载给你导进来的那个文件。

实测：amr 8488 B → 2.780 s wav、mp3 23084 B → 2.772 s、aac 19313 B → 2.880 s、wav 原样直通。

## 3. 转码产出的 mp3 去掉 ID3

跨格式转码出来的 mp3 一律带 `ID3` 前缀（ffmpeg mp3 muxer 默认写 ID3v2 + Xing/LAME 帧）。
5g 的真实服务端实测走的是「资产已匹配 → `-c copy` 直通」，**带 ID3 的 mp3 上行从未对真实服务端验证过**；
真实设备固件也不会写 ID3。

改后：mp3 加 `-id3v2_version 0 -write_xing 0`，产出裸 MPEG 帧流（`FF F3 ...`）。

这两个是**容器层参数，不能混进编码参数**：`StreamRealtime` 在 `-c:a copy` 直通时会把编码参数
整体替换掉，混在里面就一起丢了——第一版就是这么写的，实测转码产物干净、直通推流的上行
又长回了 ID3 头。因此拆出 `muxerArgs(format)`，`TranscodeFile` 与 `StreamRealtime` 两条路都追加。

## 4. 与真实服务端的格式对照（本阶段结论依据）

| 格式 | 上行 ASR | 下行编码契约 | 备注 |
|---|---|---|---|
| pcm / s16le / raw | ✅ | ✅ | |
| wav | ✅ | ✅ **回落为 pcm** | 注册表无 wav 契约，下行归入 PCM 家族（实测） |
| mp3 | ✅ | ✅ 帧对齐切分 | 5g 实测 |
| amr | ✅ | ✅ 每包带存储头 | 5g 实测 |
| aac | ✅ | ✅ 20 KB 硬切 | 实测服务端回 aac |
| opus / ogg | ✅ | ✅ 自包含 OggS 页 | 模拟器未实现 |
| speex / silk / m4a | ✅ 透传云 ASR | ❌ 无契约，且无 PCM 家族回落 | 模拟器未实现 |

下行格式跟随上行格式（`common/client/reply.go` 的 `audioConfig.Format = c.Format`），
这既是 5g 实测结论，也是代码依据。

## 5. VAD / Stage=4 实测（2026-09-03，真实服务端 VOICE-TEST）

`architecture.md` 硬约束表写着「Stage=4 → 先 vad（若空），停 Stage=1，补 Stage=2」。
这条约束此前**从未被验证**：`core.handleVADLocked` 无任何测试覆盖，echosrv 也从不发 Stage=4。
用真实身份对真实服务端跑了三发（设备 `audio.format=pcm`，因而可用 `stream` 拼接）：

| 探针 | 上行 | Stage=1 跨度 | Stage=2 | Stage=4 |
|---|---|---|---|---|
| A | 单资产 1.67 s | 1.60 s | +1.80 s | +1.98 s（晚 **0.17 s**） |
| B | 1.67 s + 静音 0.8 s + 1.59 s | 4.01 s | +4.21 s | +4.37 s（晚 **0.16 s**） |
| C | 1.67 s + 静音 3.0 s + 1.59 s | 6.21 s | +6.41 s | +6.54 s（晚 **0.12 s**） |

结论：

- **Stage=4 每轮都会来**，`vad` 事件每轮都记到了——`handleVADLocked` 一直在跑，不是死代码。
- **但它恒定晚于我们自己的 Stage=2 约 0.12–0.17 s**，此时 `slot.UplinkEnd()` 已是 `stage2`、
  状态也过了 `TurnWaitingReply`，所以该函数的三个副作用——写 `uplink_end_reason=vad`、
  `turn.frozen=true` 停发剩余 Stage=1、补发 Stage=2——**一个都没触发**，只留下事件。
- **插入静音不会让 ASR 提前 final**。服务端的 Stage=4 由云 ASR 的 `isFinal && text != ""`
  触发（`module/voiceModule/voice_module.go` 的 `sendVadFlagIfSet`），而 `asr.vad`（默认 300 ms）
  是传给厂商 ASR 的端点检测参数。实测这条链路上 ASR **只在上行流结束后**才给 final：
  0.8 s 与 3.0 s 的数字静音都没能让它在流中途断句。

因此 `uplink_end_reason=vad` 这条分支在当前服务端配置下**不可达**，
`turn.frozen` 的停发路径同样从未执行。要覆盖它只能靠模拟服务端主动发 Stage=4。
在此之前，硬约束表里那条不应被当成「已验证」。

## 6. 四格式真实服务端全通实测（2026-09-03，VOICE-TEST 真实身份）

| 设备格式 | 上行落盘首字节 | 服务端下行 | `down_format` | 何时测的 |
|---|---|---|---|---|
| pcm | 裸 s16le | pcm | `pcm` | 本阶段（VAD 三发探针） |
| wav | `RIFF` | **pcm**，156932 B / 12 帧 | `pcm` | 本阶段 |
| mp3 | `FF F3`（裸 MPEG 帧） | mp3 | `mp3` | Phase 5g |
| amr | `#!AMR-WB` | amr | `amr` | Phase 5g |
| aac | ADTS `FF F1` | **aac**，27434 B | `aac` | 本阶段 |

五种取值全部走通「注册 → Ready → 上行 → 服务端 TTS → `reply_kind=tts` / `idle` 终态」。

**唯一要知道的差异：wav 设备的下行是 pcm，不是 wav。** 先前据服务端注册表推断的
「wav 设备收不到下行」是错的——注册表确实没有 wav 契约，但下行走 PCM 家族回落，
照常有音频。`speex/silk/m4a` 没有这种回落（`Resolve` 直接失败），不能类推。

## 边界（本阶段不做）

- opus / ogg / speex / silk / m4a——`audio.format` 取值域仍是 `pcm|wav|mp3|amr|aac` 五项。
- Phase 5 的其余边界照旧不做：压缩设备的 `stream` 拼接、`bad_seq` 以外的故障注入走压缩流、
  派生副本缓存的持久化与容量上限。
- echosrv 不复刻服务端的播放水位、ACK 与 VAD；它仍只是本地验收桩。
