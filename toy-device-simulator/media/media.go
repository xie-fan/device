// Package media 封装 ffmpeg/ffprobe：格式探测、转码、-re 实时推流。
// 无 ffmpeg 环境下 Detect 返回错误，调用方自行降级
// （pcm/wav 纯 Go 路径不依赖本包）。
package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// 支持的目标格式（设备 audio.format 的取值域）。
const (
	FormatPCM = "pcm"
	FormatWAV = "wav"
	FormatMP3 = "mp3"
	FormatAMR = "amr"
	FormatAAC = "aac"
)

// TargetFormats 设备可配置的格式白名单（校验与 UI 下拉共用）。
var TargetFormats = []string{FormatPCM, FormatWAV, FormatMP3, FormatAMR, FormatAAC}

// IsTargetFormat 判断 f 是否为设备可配置格式。
func IsTargetFormat(f string) bool {
	for _, v := range TargetFormats {
		if v == f {
			return true
		}
	}
	return false
}

// Compressed 判断格式是否为压缩编码（需要码率、需要 ffmpeg 才能编解码）。
func Compressed(f string) bool {
	return f == FormatMP3 || f == FormatAMR || f == FormatAAC
}

// Spec 转码/推流的目标规格。BitrateKbps 仅压缩格式有效，<=0 取格式默认。
type Spec struct {
	Format      string
	SampleRate  int
	Channels    int
	BitrateKbps float64
}

// Info ffprobe 探测结果。Format 已归一化到本项目格式名
// （无法归一化时保留 ffprobe format_name 的首项，仅作展示/转码源）。
type Info struct {
	Format      string
	Codec       string
	SampleRate  int
	Channels    int
	BitrateKbps float64
	DurationMs  int
}

// Matches 判断探测结果是否与目标规格一致（一致则无需转码；
// 码率不参与比较：同格式同采样率同声道即可直通）。
func (i Info) Matches(s Spec) bool {
	return i.Format == s.Format && i.SampleRate == s.SampleRate && i.Channels == s.Channels
}

// Toolchain 一套可用的 ffmpeg/ffprobe 及其音频编码能力。
type Toolchain struct {
	FFmpeg   string
	FFprobe  string
	Encoders map[string]bool
}

// Detect 定位 ffmpeg/ffprobe 并探测音频编码器能力。
// ffmpegPath 为空时查 PATH；显式给出时要求 ffprobe 在同目录
// （规避 PATH 上 essentials build 缺 AMR 编码器而 full build 在别处的情况）。
func Detect(ffmpegPath string) (*Toolchain, error) {
	ffmpeg := ffmpegPath
	var ffprobe string
	if ffmpeg == "" {
		p, err := exec.LookPath("ffmpeg")
		if err != nil {
			return nil, fmt.Errorf("PATH 中未找到 ffmpeg：%w", err)
		}
		ffmpeg = p
		pp, err := exec.LookPath("ffprobe")
		if err != nil {
			return nil, fmt.Errorf("PATH 中未找到 ffprobe：%w", err)
		}
		ffprobe = pp
	} else {
		if _, err := os.Stat(ffmpeg); err != nil {
			return nil, fmt.Errorf("ffmpeg_path 无效：%w", err)
		}
		name := "ffprobe"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		ffprobe = filepath.Join(filepath.Dir(ffmpeg), name)
		if _, err := os.Stat(ffprobe); err != nil {
			return nil, fmt.Errorf("ffmpeg 同目录未找到 ffprobe：%s", ffprobe)
		}
	}
	out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg -encoders 执行失败：%w", err)
	}
	return &Toolchain{FFmpeg: ffmpeg, FFprobe: ffprobe, Encoders: parseEncoders(string(out))}, nil
}

// parseEncoders 解析 `ffmpeg -encoders` 输出，收集音频编码器名（flags 首字符 A）。
func parseEncoders(out string) map[string]bool {
	enc := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && len(f[0]) >= 1 && f[0][0] == 'A' {
			enc[f[1]] = true
		}
	}
	return enc
}

// CanEncode 校验工具链能否编码到目标格式（AMR 依采样率区分 NB/WB 编码器）。
func (t *Toolchain) CanEncode(format string, sampleRate int) error {
	need := ""
	switch format {
	case FormatPCM, FormatWAV:
		return nil
	case FormatMP3:
		need = "libmp3lame"
	case FormatAAC:
		need = "aac"
	case FormatAMR:
		if sampleRate == 8000 {
			need = "libopencore_amrnb"
		} else {
			need = "libvo_amrwbenc"
		}
	default:
		return fmt.Errorf("不支持的目标格式 %q", format)
	}
	if !t.Encoders[need] {
		return fmt.Errorf("当前 ffmpeg 缺少编码器 %s（%s）：请安装 full build 并在 manager.yaml 配置 ffmpeg_path", need, filepath.Base(t.FFmpeg))
	}
	return nil
}

// Capabilities 返回目标格式的可编码性摘要（启动日志用）。
func (t *Toolchain) Capabilities() string {
	var parts []string
	for _, f := range TargetFormats {
		sr := 0
		if f == FormatAMR {
			sr = 8000
		}
		mark := "+"
		if t.CanEncode(f, sr) != nil {
			mark = "-"
		}
		if f == FormatAMR {
			wb := "+"
			if t.CanEncode(f, 16000) != nil {
				wb = "-"
			}
			parts = append(parts, mark+"amr-nb", wb+"amr-wb")
			continue
		}
		parts = append(parts, mark+f)
	}
	return strings.Join(parts, " ")
}

// amr 合法码率档位（kbps）；ffmpeg 的 opencore/vo 编码器要求精确匹配。
var (
	amrNBRates = []float64{4.75, 5.15, 5.9, 6.7, 7.4, 7.95, 10.2, 12.2}
	amrWBRates = []float64{6.6, 8.85, 12.65, 14.25, 15.85, 18.25, 19.85, 23.05, 23.85}
)

// nearestRate 就近取合法档位（表必须升序）。
func nearestRate(kbps float64, table []float64) float64 {
	i := sort.SearchFloat64s(table, kbps)
	if i == 0 {
		return table[0]
	}
	if i == len(table) {
		return table[len(table)-1]
	}
	if kbps-table[i-1] <= table[i]-kbps {
		return table[i-1]
	}
	return table[i]
}

// defaultBitrate 各压缩格式的默认码率（kbps）。
func defaultBitrate(format string, sampleRate int) float64 {
	switch format {
	case FormatMP3:
		return 128
	case FormatAAC:
		return 96
	case FormatAMR:
		if sampleRate == 8000 {
			return 12.2
		}
		return 23.85
	}
	return 0
}

// NormalizeBitrate 归一化码率：<=0 补默认，amr 就近合法档位。
// config 校验与转码共用，保证配置里看到的就是实际编码用的。
func NormalizeBitrate(format string, sampleRate int, kbps float64) float64 {
	if !Compressed(format) {
		return 0
	}
	if kbps <= 0 {
		kbps = defaultBitrate(format, sampleRate)
	}
	if format == FormatAMR {
		if sampleRate == 8000 {
			return nearestRate(kbps, amrNBRates)
		}
		return nearestRate(kbps, amrWBRates)
	}
	return kbps
}

// muxerArgs 容器层参数，与编码参数分开：`-c:a copy` 直通时编码参数被整体替换，
// 混在里面会一起丢掉（Phase 6 实测：直通推流的 mp3 又长回了 ID3 头）。
// mp3 muxer 默认写 ID3v2 与 Xing/LAME 帧，真实设备固件不会，带上就不忠实。
func muxerArgs(format string) []string {
	if format == FormatMP3 {
		return []string{"-id3v2_version", "0", "-write_xing", "0"}
	}
	return nil
}

// encodeArgs 目标规格 → (编码参数, 容器/裸流 mux 名)。
func encodeArgs(spec Spec) (codecArgs []string, mux string, err error) {
	kbps := NormalizeBitrate(spec.Format, spec.SampleRate, spec.BitrateKbps)
	br := fmt.Sprintf("%gk", kbps)
	switch spec.Format {
	case FormatPCM:
		return []string{"-c:a", "pcm_s16le"}, "s16le", nil
	case FormatWAV:
		return []string{"-c:a", "pcm_s16le"}, "wav", nil
	case FormatMP3:
		return []string{"-c:a", "libmp3lame", "-b:a", br}, "mp3", nil
	case FormatAAC:
		return []string{"-c:a", "aac", "-b:a", br}, "adts", nil
	case FormatAMR:
		switch spec.SampleRate {
		case 8000:
			return []string{"-c:a", "libopencore_amrnb", "-b:a", br}, "amr", nil
		case 16000:
			return []string{"-c:a", "libvo_amrwbenc", "-b:a", br}, "amr", nil
		default:
			return nil, "", fmt.Errorf("amr 采样率仅支持 8000（NB）/16000（WB），得到 %d", spec.SampleRate)
		}
	default:
		return nil, "", fmt.Errorf("不支持的目标格式 %q", spec.Format)
	}
}

// normalizeFormat ffprobe format_name/codec → 项目格式名。
func normalizeFormat(formatName, codec string) string {
	names := strings.Split(formatName, ",")
	for _, f := range names {
		switch f {
		case "wav":
			return FormatWAV
		case "mp3":
			return FormatMP3
		case "amr":
			return FormatAMR
		case "aac":
			return FormatAAC
		}
	}
	if strings.HasPrefix(codec, "pcm_") {
		return FormatPCM
	}
	return names[0]
}
