package manager

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"toy-device-simulator/config"
)

// LoadDeviceYAML 解析 Phase 2 设备 YAML。出现 write_queue_* 即拒绝（即使值在 Phase 1 合法）。
func LoadDeviceYAML(raw []byte) (config.Device, error) {
	if yamlHasKey(raw, "write_queue_depth") || yamlHasKey(raw, "write_drain_timeout_sec") {
		return config.Device{}, fmt.Errorf("Phase 2 设备配置禁止 write_queue_depth / write_drain_timeout_sec")
	}
	var f config.File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return config.Device{}, err
	}
	return f.Device, nil
}

func yamlHasKey(raw []byte, key string) bool {
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return false
	}
	return valueHasKey(v, key)
}

func valueHasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == key {
				return true
			}
			if valueHasKey(val, key) {
				return true
			}
		}
	case map[any]any:
		for k, val := range t {
			if ks, ok := k.(string); ok && ks == key {
				return true
			}
			if valueHasKey(val, key) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if valueHasKey(val, key) {
				return true
			}
		}
	}
	return false
}
