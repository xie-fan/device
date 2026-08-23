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
	return c, nil
}

func LoadFile(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Load(raw)
}
