package config

import (
	"fmt"
	"strconv"
	"strings"

	"toy-device-simulator/media"
)

type Product struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	PlayingModes []int    `yaml:"playing_modes"`
	AudioFormats []string `yaml:"audio_formats"` // "格式/采样率"
	Defaults     Device   `yaml:"defaults"`
}

type Features struct {
	Photo PhotoFeature `yaml:"photo"`
}

type PhotoFeature struct {
	Enabled            bool   `yaml:"enabled"`
	Image              string `yaml:"image"`                // 图片资产 id
	ServerDefaultReply bool   `yaml:"server_default_reply"` // true：Reserved 留空，服务端按默认回 aac
	SliceIntervalMs    int    `yaml:"slice_interval_ms"`    // 0 = 50ms
	ReplyTimeoutSec    int    `yaml:"reply_timeout_sec"`    // 0 = 60s
}

func boolPtr(v bool) *bool { return &v }

// DefaultProduct 内置基线：值取自原 default_a3 模板；recording.output_dir 留空、write_queue_* 为 0，合成时再补。
func DefaultProduct() Product {
	return Product{
		ID:           "default",
		Name:         "默认产品",
		PlayingModes: []int{1, 2, 3},
		AudioFormats: []string{"pcm/16000", "wav/16000", "mp3/16000", "amr/16000", "aac/16000"},
		Defaults: Device{
			Action:          "chatbot",
			FirmwareVersion: "1.0.0",
			NicType:         "wifi",
			NicICCID:        "8986xxxxxxxxxx",
			PlayingMode:     1,
			Audio: Audio{
				Format:         "pcm",
				SampleRate:     16000,
				Channels:       1,
				SampleFormat:   "s16le",
				SliceMs:        100,
				MaxPayloadSize: 51200,
			},
			Behavior: Behavior{
				AutoRegister:           boolPtr(true),
				AutoReport:             boolPtr(true),
				KeepaliveIntervalSec:   60,
				KeepaliveMethod:        "report",
				ReportSequenceStart:    1,
				ReportEchoTimeoutSec:   5,
				RegisterAckTimeoutSec:  5,
				FirstReplyTimeoutSec:   20,
				DownlinkIdleTimeoutSec: 20,
				NonAudioFollowupSec:    5,
				PostFinalASRSilenceSec: 5,
				WaitTimeoutSlackSec:    5,
				DownlinkAck:            DownlinkAck{Mode: "binary", SleepMs: 0, Code: 0},
			},
			UUID: UUIDRange{Min: 1, Max: 2147483647},
			Recording: Recording{
				EnableFrameLog:    true,
				SaveUplinkAudio:   true,
				SaveDownlinkAudio: true,
			},
		},
	}
}

func ParseAudioSpec(s string) (format string, sampleRate int, err error) {
	if strings.Count(s, "/") != 1 {
		return "", 0, fmt.Errorf("音频规格须为 格式/采样率，得到 %q", s)
	}
	format, rest, _ := strings.Cut(s, "/")
	if format == "" || rest == "" {
		return "", 0, fmt.Errorf("音频规格须为 格式/采样率，得到 %q", s)
	}
	if !media.IsTargetFormat(format) {
		return "", 0, fmt.Errorf("format 仅支持 %s，得到 %q", strings.Join(media.TargetFormats, "/"), format)
	}
	sr, convErr := strconv.Atoi(rest)
	if convErr != nil || sr <= 0 {
		return "", 0, fmt.Errorf("采样率必须是正整数，得到 %q", rest)
	}
	if format == media.FormatAMR && sr != 8000 && sr != 16000 {
		return "", 0, fmt.Errorf("amr 采样率仅支持 8000/16000，得到 %d", sr)
	}
	return format, sr, nil
}

func ValidateProduct(p Product) error {
	if err := ValidatePathComponent(p.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name 必填")
	}
	if len(p.PlayingModes) == 0 {
		return fmt.Errorf("playing_modes 不得为空")
	}
	seenMode := map[int]bool{}
	for _, m := range p.PlayingModes {
		if m < 1 || m > 3 {
			return fmt.Errorf("非法 playing_mode=%d", m)
		}
		if seenMode[m] {
			return fmt.Errorf("playing_modes 重复 %d", m)
		}
		seenMode[m] = true
	}
	if len(p.AudioFormats) == 0 {
		return fmt.Errorf("audio_formats 不得为空")
	}
	seenFmt := map[string]bool{}
	for _, spec := range p.AudioFormats {
		if _, _, err := ParseAudioSpec(spec); err != nil {
			return err
		}
		if seenFmt[spec] {
			return fmt.Errorf("audio_formats 重复 %s", spec)
		}
		seenFmt[spec] = true
	}
	d := p.Defaults
	if d.DeviceID != "" || d.Enterprise != "" || d.DeviceType != "" || d.Server.URL != "" {
		return fmt.Errorf("产品默认值不得带 device_id / enterprise / device_type / server")
	}
	if d.Features.Photo.SliceIntervalMs < 0 {
		return fmt.Errorf("features.photo.slice_interval_ms 不得为负")
	}
	if d.Features.Photo.ReplyTimeoutSec < 0 {
		return fmt.Errorf("features.photo.reply_timeout_sec 不得为负")
	}
	book := d
	book.DeviceID = "product"
	book.Behavior.WriteQueueDepth = 2
	book.Behavior.WriteDrainTimeoutSec = 1
	applyPhase1Defaults(&book)
	if err := ValidateBookEntry(book); err != nil {
		return err
	}
	if !seenMode[d.PlayingMode] {
		return fmt.Errorf("默认 playing_mode=%d 不在清单内", d.PlayingMode)
	}
	audioSpec := fmt.Sprintf("%s/%d", d.Audio.Format, d.Audio.SampleRate)
	if !seenFmt[audioSpec] {
		return fmt.Errorf("默认音频 %s 不在清单内", audioSpec)
	}
	return nil
}
