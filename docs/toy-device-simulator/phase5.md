# Phase 5 多格式音频与 ffmpeg 管线

Phase 5 打破 Phase 4 的「mp3 留待真实需求」：设备可配置压缩线上格式（mp3/amr/aac），任意常见格式的音频可入库并喂给任意设备，格式不符自动经 ffmpeg 转码；压缩格式上行由 ffmpeg `-re` 按真实播放速率限速推流。pcm/wav 保持纯 Go 管线，**无 ffmpeg 环境下 Phase 1–4 全部能力不回退**。

## ffmpeg 工具链（media 包）

- `manager.yaml` 增 `ffmpeg_path`（空 = 查 PATH；显式给出时要求 ffprobe 在同目录）。启动时探测并打印编码能力：`+pcm +wav +mp3 +amr-nb +amr-wb +aac`（`-` 表示缺编码器）。
- 编码器要求：mp3 → `libmp3lame`；aac → `aac`；amr-nb（8000 Hz）→ `libopencore_amrnb`；amr-wb（16000 Hz）→ `libvo_amrwbenc`。essentials 构建常缺 AMR，需 full 构建并配 `ffmpeg_path`。
- 三个原语：`Probe`（ffprobe -of json，归一化到项目格式名）、`TranscodeFile`（一次性转码，直接写文件回填容器头）、`StreamRealtime`（`-re -readrate_initial_burst 0.0`，按输入实际速率产出目标格式裸流；返回的 ReadCloser 关闭即杀进程）。
- Windows 取消策略：`taskkill /F /T` 杀整棵进程树（PATH 上常见 chocolatey shim 包装器，`Process.Kill` 只能杀外壳）；`WaitDelay` 3s 防 Wait 永久阻塞。
- 无 ffmpeg：`Detect` 返回错误，manager 照常启动（日志提示）；仅 pcm/wav + WAV/raw PCM 上传可用，其余路径 400 并解释缺什么。

## 设备音频配置

- `audio.format` 取值域扩为 `pcm | wav | mp3 | amr | aac`（Phase 1 CLI 仍仅 pcm）。
- 新增 `audio.bitrate_kbps`（float，仅压缩格式有效；非压缩格式必须为 0/缺省）。<=0 取格式默认：mp3 128、aac 96、amr-nb 12.2、amr-wb 23.85。amr 码率就近归一到合法档位（NB 4.75–12.2 / WB 6.6–23.85 八九档），**配置校验与实际编码共用同一归一化**，配置里看到的就是编码用的。
- amr 采样率仅 8000（NB）/ 16000（WB），其余 400。
- 协议头 `AudioFormat` 字段写设备实际格式（`NewAudioHeader(format, ...)`；`NewPCMHeader` 为 format=pcm 的兼容包装）。

## 音频库（资产升级为持久库）

存储：`assets_root/index.json` + `{asset_id}.{ext}`（原格式保存）+ `{asset_id}.v.{fp}.{ext}`（选用时的转码派生副本缓存，不进 index，重启清孤儿）。

- `POST /assets` multipart：`file` 必填；可选 `name`（默认文件名）、`language`、`device_id`。有 ffmpeg 时任意 ffprobe 可识别的音频均可入库（探测格式/采样率/声道/码率/时长）；无 ffmpeg 退回 WAV/raw PCM（Phase 4 语义）。raw PCM 三参数（`sample_rate`/`channels`/`sample_format`）语义不变。
  - 带 `device_id`：入库即转码到该设备当前音频规格（不存在 → 404；已匹配则直通），响应含 `transcoded:true`。
  - 不带：原格式入库，选用时按需转码。
- `GET /assets?format=&language=`：列表（按 created_at 排序），响应含 `asset_id/name/language/format/sample_rate/channels/bitrate_kbps/duration_ms/bytes/epoch`。
- `PATCH /assets/{id}`：仅 `name`/`language` 可改，未知键 400。
- `GET /assets/{id}/content`：原始字节，Content-Type 按格式；`http.ServeContent` 支持 Range（浏览器媒体栈探测 mp3 时长需 seek 文件尾）。
- `DELETE /assets/{id}`：epoch++、删原文件与全部派生副本、持久化 index。epoch 复验语义不变（architecture.md §4.1）。
- index.json 在 manager 启动时装载：库跨重启存活；派生副本缓存不持久（键含设备规格指纹，命中即复用，miss 重转）。

## speak 按设备格式解析

speak 受理在 Phase 2 合同（architecture.md §4.1 受理顺序）之上叠加格式解析，占槽前完成：

- **pcm/wav 设备**：资产已是匹配 WAV（s16le、采样率声道相符）→ 原快路径（epoch 复验拷贝 + DecodeWAV）。否则经派生副本取 WAV（transcode 缓存）再 DecodeWAV。`stream` 拼接照旧（silence 生成 + 多资产逐段解析）。上行仍是预构建帧 + Go 定时 pace（wav 整段一次 RIFF 头再切片，Phase 4f 语义不变）。
- **压缩设备（mp3/amr/aac）**：`stream` 拼接 400（`asset_id` 单资产）；无 ffmpeg 400。资产先解析到设备规格的文件（原生匹配或派生副本），speak 时 `StreamRealtime -c copy` 限速直通，边读边按 `chunk ≈ bitrate × slice_ms` 聚合帧化上行（`core.SpeakStreamPermit`）。**发送节奏由 ffmpeg 限速给出，Go 侧不再 pace**。等待预算按 ffprobe 时长计（`WaitBudgetForDuration`）。
  - 帧化/锁检查/冻结/排空/backlog 语义与 pcm 管线一致；故障注入仅 `bad_seq` 对齐（其余注入仍走 pcm/wav 预构建路径）。
  - finalize/interrupt 即杀 ffmpeg 进程（不等下一次 Read）；推流源失败 → 冻结 + 取消 + `EndError` 收口，连接保留。

## 下行与回放格式感知

- 下行 TTS 首帧头的 `AudioFormat`/`SamplingRate` 记入 turn.json（`down_format`/`down_sample_rate`）；落盘字节保持原样追加（`downlink.pcm` 文件名沿用）。不再假定下行是 PCM。
- `GET .../audio/uplink|downlink`：上行格式 = 设备 format；下行以 turn.json 为准（缺省：压缩设备 → 设备格式，否则 pcm）。默认响应解码为 WAV 供浏览器试听（压缩格式经 ffmpeg，无 ffmpeg 400 提示 `?raw=1`）；`?raw=1` 原始字节 + 对应 Content-Type。wav 设备上行内容本身含 RIFF 头，原样返回（修复历史双重包头）。

## UI

- 左栏「音频库」面板：列表（名称/格式/采样率/码率/时长/语言）、格式与语言筛选、导入表单、行内编辑（名称/语言）、两击删除、试听（wav/mp3/aac 原生可播；pcm/amr 禁用并提示）。
- speak 区「从库选择」下拉 + 送出所选：任意格式资产喂当前设备。上传送出改带 `device_id`（入库即转码）。
- 设备配置表单：`audio.format` 下拉含 mp3/amr/aac，压缩格式露出 `bitrate_kbps`。

## 真实服务端实测（5g）

- pcm 基线不回归；mp3/amr 设备对真实服务端的注册、上行推流、下行回复行为记录于验收笔记（服务端对压缩格式的支持程度以实测为准，模拟器侧不做假设）。

## 边界（本阶段不做）

- 压缩设备的 `stream` 拼接（silence/多段）；除 `bad_seq` 外的故障注入走压缩流。
- 派生副本缓存的持久化与容量上限（重启即弃，够用）。
- 浏览器端 amr/pcm 原生试听（可下载或用 `?raw=1`）。
