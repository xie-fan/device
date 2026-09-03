package api

// 设备定义的落盘。设备原本只活在 manager 内存里，进程一停全丢；而设备配置主要
// 是给 agent 消费的，agent 应当能按 device_id 直接引用一台配好的设备，不必每次重建。
//
// 存的是「定义」不是「运行状态」：加载回来一律是 created，不自动 start；
// instance_id、conn_generation、turn、事件都仍然是每次进程内新生成的（世系模型不变，
// 所以重启后旧 instance 的录音 API 取不到——这是已知取舍，见 phase7.md）。
//
// 格式用 YAML 而非 JSON：config.Device 只有 yaml tag，且 Behavior 里用 *bool 区分
// 「省略」与「显式 false」，yaml 能原样往返，换 JSON 要给 6 个结构体补一套 tag。
//
// ponytail: 整份重写，够几十台设备用；等到要按条件查、或多进程并发写，再换 sqlite。

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"toy-device-simulator/config"
)

type deviceStoreEntry struct {
	Environment string        `yaml:"environment"`
	Device      config.Device `yaml:"device"`
}

type devicesStoreFile struct {
	Devices []deviceStoreEntry `yaml:"devices"`
}

// devicesStorePath 跟随 assets_root 的父目录（默认 ./data），
// 这样测试只要换 assets_root 就自动隔离，不用再加一个配置项。
func (s *Server) devicesStorePath() string {
	return filepath.Join(filepath.Dir(s.assetsRoot()), "devices.yaml")
}

// loadDevices 启动时重建设备清单（api.New 调用，无并发）。
// 任何一条读坏就跳过该条，不阻止 manager 启动——调试台不该因为一台设备的
// 脏定义整个起不来。
func (s *Server) loadDevices() {
	raw, err := os.ReadFile(s.devicesStorePath())
	if err != nil {
		return
	}
	var f devicesStoreFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return
	}
	for _, e := range f.Devices {
		cfg := e.Device
		if cfg.DeviceID == "" {
			continue
		}
		// 运输层参数以当前 manager.yaml 为准，不用盘上的旧值。
		cfg.Behavior.WriteQueueDepth = s.opts.Config.WriteQueueDepth
		cfg.Behavior.WriteDrainTimeoutSec = s.opts.Config.WriteDrainTimeoutSec
		if err := config.ValidatePhase2(cfg); err != nil {
			continue
		}
		if _, dup := s.devices[cfg.DeviceID]; dup {
			continue
		}
		s.devices[cfg.DeviceID] = s.newManaged(cfg, e.Environment)
	}
}

// persistDevicesLocked 重写设备定义文件；调用方必须持 s.mu。
func (s *Server) persistDevicesLocked() {
	f := devicesStoreFile{Devices: make([]deviceStoreEntry, 0, len(s.devices))}
	for _, d := range s.devices {
		f.Devices = append(f.Devices, deviceStoreEntry{Environment: d.envName, Device: d.cfg})
	}
	sort.Slice(f.Devices, func(i, j int) bool {
		return f.Devices[i].Device.DeviceID < f.Devices[j].Device.DeviceID
	})
	raw, err := yaml.Marshal(f)
	if err != nil {
		return
	}
	path := s.devicesStorePath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	_ = os.WriteFile(path, raw, 0o644)
}
