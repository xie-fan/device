package manager

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 对应 Manager YAML 的 manager: 段。
type Config struct {
	MaxConnections        int    `yaml:"max_connections"`
	MaxConcurrentSpeaking int    `yaml:"max_concurrent_speaking"`
	DefaultStaggerMs      int    `yaml:"default_stagger_ms"`
	PerDeviceBufferBytes  int    `yaml:"per_device_buffer_bytes"`
	WriteQueueDepth       int    `yaml:"write_queue_depth"`
	WriteDrainTimeoutSec  int    `yaml:"write_drain_timeout_sec"`
	EventLogMaxEntries    int    `yaml:"event_log_max_entries"`
	EventLogTTLHours      int    `yaml:"event_log_ttl_hours"`
	AssetsRoot            string `yaml:"assets_root"`
	MaxAssetBytes         int64  `yaml:"max_asset_bytes"`
	MaxAssetDurationSec   int    `yaml:"max_asset_duration_sec"`
	MaxStreamEntries      int    `yaml:"max_stream_entries"`
	MaxStreamDurationSec  int    `yaml:"max_stream_duration_sec"`
	WaitReadyTimeoutSec   int    `yaml:"wait_ready_timeout_sec"`
	// FFmpegPath ffmpeg 可执行文件路径；空 = 查 PATH。
	// ffprobe 要求与 ffmpeg 同目录（显式指定时）。
	FFmpegPath string `yaml:"ffmpeg_path"`
}

type file struct {
	Manager Config `yaml:"manager"`
}

func Load(raw []byte) (Config, error) {
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return Config{}, err
	}
	c := f.Manager
	// 不得给 depth 填缺省来绕过「< 2 失败」的测试。
	if c.WriteQueueDepth < 2 {
		return Config{}, fmt.Errorf("write_queue_depth 必须 >= 2，得到 %d", c.WriteQueueDepth)
	}
	if c.WriteDrainTimeoutSec <= 0 {
		return Config{}, fmt.Errorf("write_drain_timeout_sec 必须 > 0，得到 %d", c.WriteDrainTimeoutSec)
	}
	if c.DefaultStaggerMs == 0 {
		c.DefaultStaggerMs = 50
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 32
	}
	if c.MaxConcurrentSpeaking == 0 {
		c.MaxConcurrentSpeaking = 8
	}
	if c.PerDeviceBufferBytes == 0 {
		c.PerDeviceBufferBytes = 1048576
	}
	if c.EventLogMaxEntries == 0 {
		c.EventLogMaxEntries = 10000
	}
	if c.EventLogTTLHours == 0 {
		c.EventLogTTLHours = 24
	}
	if c.AssetsRoot == "" {
		c.AssetsRoot = "./data/assets"
	}
	if c.MaxAssetBytes == 0 {
		c.MaxAssetBytes = 10485760
	}
	if c.MaxAssetDurationSec == 0 {
		c.MaxAssetDurationSec = 60
	}
	if c.MaxStreamEntries == 0 {
		c.MaxStreamEntries = 16
	}
	if c.MaxStreamDurationSec == 0 {
		c.MaxStreamDurationSec = 60
	}
	if c.WaitReadyTimeoutSec == 0 {
		c.WaitReadyTimeoutSec = 30
	}
	return c, nil
}

func LoadFile(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Load(raw)
}
