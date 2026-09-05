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
	"bytes"
	"fmt"
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
	path := s.devicesStorePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		// 首次启动没有这个文件是正常的；其它读失败（权限、损坏）必须出声。
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "devices store: 读不了 %s: %v（本次启动没有任何设备）\n", path, err)
		}
		return
	}
	var f devicesStoreFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		fmt.Fprintf(os.Stderr, "devices store: %s 解析失败: %v（本次启动没有任何设备）\n", path, err)
		return
	}
	// 跳过脏定义不阻止启动，但必须可观测：只看到「设备少了」而不知道少了几台、
	// 为什么少，会把数据丢失伪装成正常状态。
	loaded, skipped := 0, 0
	skip := func(id, why string) {
		skipped++
		if id == "" {
			id = "(无 device_id)"
		}
		fmt.Fprintf(os.Stderr, "devices store: 跳过 %s：%s\n", id, why)
	}
	for _, e := range f.Devices {
		cfg := e.Device
		if cfg.DeviceID == "" {
			skip("", "缺 device_id")
			continue
		}
		// 运输层参数以当前 manager.yaml 为准，不用盘上的旧值。
		cfg.Behavior.WriteQueueDepth = s.opts.Config.WriteQueueDepth
		cfg.Behavior.WriteDrainTimeoutSec = s.opts.Config.WriteDrainTimeoutSec
		if err := config.ValidatePhase2(cfg); err != nil {
			skip(cfg.DeviceID, err.Error())
			continue
		}
		if _, dup := s.devices[cfg.DeviceID]; dup {
			skip(cfg.DeviceID, "device_id 重复")
			continue
		}
		s.devices[cfg.DeviceID] = s.newManaged(cfg, e.Environment)
		loaded++
	}
	fmt.Fprintf(os.Stderr, "devices store: loaded=%d skipped=%d\n", loaded, skipped)
}

// persistDevicesLocked 重写设备定义文件；调用方必须持 s.mu。
func (s *Server) persistDevicesLocked() error {
	f := devicesStoreFile{Devices: make([]deviceStoreEntry, 0, len(s.devices))}
	for _, d := range s.devices {
		f.Devices = append(f.Devices, deviceStoreEntry{Environment: d.defEnv, Device: d.def})
	}
	sort.Slice(f.Devices, func(i, j int) bool {
		return f.Devices[i].Device.DeviceID < f.Devices[j].Device.DeviceID
	})
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return atomicWrite(s.devicesStorePath(), raw)
}

// overridden 当前值是否偏离落盘定义。比 yaml 字节而不是 reflect.DeepEqual：
// Behavior 里有 *bool，比指针的语义不是我们要的。调用方须持 s.mu。
func (d *managedDevice) overridden() bool {
	if d.envName != d.defEnv {
		return true
	}
	a, err1 := yaml.Marshal(d.cfg)
	b, err2 := yaml.Marshal(d.def)
	if err1 != nil || err2 != nil {
		return false
	}
	return !bytes.Equal(a, b)
}

// resetToDefinitionLocked 丢弃临时修改，回到落盘定义。调用方须持 s.mu。
func (d *managedDevice) resetToDefinitionLocked() {
	d.cfg = d.def
	d.envName = d.defEnv
}
