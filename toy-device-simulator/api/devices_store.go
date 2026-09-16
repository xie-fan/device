package api

// 设备册：跨重启只存 device_id。属性归产品，挂靠归 start。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// deviceStoreEntry 新形状只有 device_id；旧形状套在 device 里，读时只取 id。
type deviceStoreEntry struct {
	DeviceID string `yaml:"device_id"`
	Device   struct {
		DeviceID string `yaml:"device_id"`
	} `yaml:"device"`
}

type devicesStoreFile struct {
	Devices []deviceStoreEntry `yaml:"devices"`
}

func (e deviceStoreEntry) id() string {
	if e.DeviceID != "" {
		return e.DeviceID
	}
	return e.Device.DeviceID
}

func (s *Server) devicesStorePath() string {
	return filepath.Join(filepath.Dir(s.assetsRoot()), "devices.yaml")
}

func (s *Server) loadDevices() {
	path := s.devicesStorePath()
	raw, err := os.ReadFile(path)
	if err != nil {
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
	loaded, skipped := 0, 0
	skip := func(id, why string) {
		skipped++
		if id == "" {
			id = "(无 device_id)"
		}
		fmt.Fprintf(os.Stderr, "devices store: 跳过 %s：%s\n", id, why)
	}
	for _, e := range f.Devices {
		id := e.id()
		if id == "" {
			skip("", "缺 device_id")
			continue
		}
		if _, dup := s.devices[id]; dup {
			skip(id, "device_id 重复")
			continue
		}
		s.devices[id] = s.newManaged(id)
		loaded++
	}
	fmt.Fprintf(os.Stderr, "devices store: loaded=%d skipped=%d\n", loaded, skipped)
}

func (s *Server) persistDevicesLocked() error {
	f := devicesStoreFile{Devices: make([]deviceStoreEntry, 0, len(s.devices))}
	for _, d := range s.devices {
		f.Devices = append(f.Devices, deviceStoreEntry{DeviceID: d.id})
	}
	sort.Slice(f.Devices, func(i, j int) bool {
		return f.Devices[i].DeviceID < f.Devices[j].DeviceID
	})
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return atomicWrite(s.devicesStorePath(), raw)
}
