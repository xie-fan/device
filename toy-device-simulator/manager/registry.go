package manager

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"toy-device-simulator/config"
)

// 配置树：环境 → 厂商（enterprise）→ 设备类型。设备挂在类型下，本体在 /devices。
// 键（环境名、两级简称）创建后不可改：改键 = 删掉重建；有引用时删不掉，
// 因此 live 设备的树引用不会悬空。

var (
	ErrRegistryNotFound = errors.New("registry: not found")
	ErrRegistryConflict = errors.New("registry: conflict")
)

type DeviceType struct {
	Name      string `yaml:"name" json:"name"`
	ShortName string `yaml:"short_name" json:"short_name"`
}

type Enterprise struct {
	Name        string       `yaml:"name" json:"name"`
	ShortName   string       `yaml:"short_name" json:"short_name"`
	DeviceTypes []DeviceType `yaml:"device_types" json:"device_types"`
}

type Environment struct {
	Name        string       `yaml:"name" json:"name"`
	URL         string       `yaml:"url" json:"url"`
	Enterprises []Enterprise `yaml:"enterprises" json:"enterprises"`
}

type registryFile struct {
	Environments []Environment `yaml:"environments"`
}

// Registry 持锁维护配置树，每次变更后原子落盘（临时文件 + rename）。
// 锁序：registry 锁是 manager 锁之后的叶子锁；方法内不回调任何会加锁的一方。
type Registry struct {
	mu   sync.Mutex
	path string
	envs []Environment
}

func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	var f registryFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("registry 解析失败: %w", err)
	}
	// 落盘内容同样过校验：手改文件不能绕过键规则。
	if err := validateTree(f.Environments); err != nil {
		return nil, fmt.Errorf("registry %s: %w", path, err)
	}
	r.envs = f.Environments
	return r, nil
}

func validateTree(envs []Environment) error {
	seenEnv := map[string]bool{}
	for _, env := range envs {
		if err := validateEnvName(env.Name); err != nil {
			return err
		}
		if err := ValidateEnvURL(env.URL); err != nil {
			return fmt.Errorf("环境 %s: %w", env.Name, err)
		}
		if seenEnv[env.Name] {
			return fmt.Errorf("环境 %s 重复", env.Name)
		}
		seenEnv[env.Name] = true
		seenEnt := map[string]bool{}
		for _, ent := range env.Enterprises {
			if err := validateEnterprise(ent.Name, ent.ShortName); err != nil {
				return err
			}
			if seenEnt[ent.ShortName] {
				return fmt.Errorf("厂商简称 %s 在环境 %s 内重复", ent.ShortName, env.Name)
			}
			seenEnt[ent.ShortName] = true
			seenTyp := map[string]bool{}
			for _, dt := range ent.DeviceTypes {
				if err := validateDeviceType(dt.Name, dt.ShortName); err != nil {
					return err
				}
				if seenTyp[dt.ShortName] {
					return fmt.Errorf("类型简称 %s 在厂商 %s 内重复", dt.ShortName, ent.ShortName)
				}
				seenTyp[dt.ShortName] = true
			}
		}
	}
	return nil
}

func validateEnvName(name string) error {
	if err := config.ValidatePathComponent(name); err != nil {
		return fmt.Errorf("环境名: %w", err)
	}
	return nil
}

func validateEnterprise(name, short string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("厂商名称必填")
	}
	if err := config.ValidatePathComponent(short); err != nil {
		return fmt.Errorf("厂商简称: %w", err)
	}
	return nil
}

func validateDeviceType(name, short string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("类型名称必填")
	}
	if err := config.ValidatePathComponent(short); err != nil {
		return fmt.Errorf("类型简称: %w", err)
	}
	// MH 前缀命中 core 的 Seq 不重置例外，用例会假通过；与设备校验同一条硬规则。
	if strings.HasPrefix(short, "MH") {
		return fmt.Errorf("类型简称不得以 MH 前缀开头")
	}
	return nil
}

var urlPlaceholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

// ValidateEnvURL 校验环境 url：占位符只认 {enterprise}/{device_type}/{device_id}，
// 代入样例值后必须是合法 ws:// 或 wss://。
func ValidateEnvURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("url 必填")
	}
	for _, m := range urlPlaceholderRe.FindAllStringSubmatch(raw, -1) {
		switch m[1] {
		case "enterprise", "device_type", "device_id":
		default:
			return fmt.Errorf("url 占位符只允许 {enterprise}/{device_type}/{device_id}，得到 {%s}", m[1])
		}
	}
	probe := SubstituteURL(raw, "e", "t", "d")
	u, err := url.Parse(probe)
	if err != nil {
		return fmt.Errorf("url 非法: %v", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("url 必须是 ws:// 或 wss://，得到 %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url 缺 host")
	}
	return nil
}

// SubstituteURL 把环境 url 里的占位符代入厂商简称 / 类型简称 / 设备名。
func SubstituteURL(raw, entShort, typeShort, deviceID string) string {
	return strings.NewReplacer(
		"{enterprise}", entShort,
		"{device_type}", typeShort,
		"{device_id}", deviceID,
	).Replace(raw)
}

// saveLocked 原子落盘。调用方须持 r.mu。
func (r *Registry) saveLocked() error {
	raw, err := yaml.Marshal(registryFile{Environments: r.envs})
	if err != nil {
		return err
	}
	if dir := filepath.Dir(r.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// Snapshot 返回整棵树的深拷贝，供 GET /registry 与 UI 消费。
func (r *Registry) Snapshot() []Environment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Environment, len(r.envs))
	for i, env := range r.envs {
		cp := env
		cp.Enterprises = make([]Enterprise, len(env.Enterprises))
		for j, ent := range env.Enterprises {
			ecp := ent
			ecp.DeviceTypes = append([]DeviceType(nil), ent.DeviceTypes...)
			cp.Enterprises[j] = ecp
		}
		out[i] = cp
	}
	return out
}

func (r *Registry) findEnvLocked(name string) *Environment {
	for i := range r.envs {
		if r.envs[i].Name == name {
			return &r.envs[i]
		}
	}
	return nil
}

func findEntLocked(env *Environment, short string) *Enterprise {
	for i := range env.Enterprises {
		if env.Enterprises[i].ShortName == short {
			return &env.Enterprises[i]
		}
	}
	return nil
}

func findTypeLocked(ent *Enterprise, short string) *DeviceType {
	for i := range ent.DeviceTypes {
		if ent.DeviceTypes[i].ShortName == short {
			return &ent.DeviceTypes[i]
		}
	}
	return nil
}

func (r *Registry) AddEnvironment(name, rawURL string) error {
	if err := validateEnvName(name); err != nil {
		return err
	}
	if err := ValidateEnvURL(rawURL); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findEnvLocked(name) != nil {
		return fmt.Errorf("%w: 环境 %s 已存在", ErrRegistryConflict, name)
	}
	r.envs = append(r.envs, Environment{Name: name, URL: rawURL})
	return r.saveLocked()
}

func (r *Registry) UpdateEnvironmentURL(name, rawURL string) error {
	if err := ValidateEnvURL(rawURL); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(name)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, name)
	}
	env.URL = rawURL
	return r.saveLocked()
}

func (r *Registry) DeleteEnvironment(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.envs {
		if r.envs[i].Name != name {
			continue
		}
		if len(r.envs[i].Enterprises) > 0 {
			return fmt.Errorf("%w: 环境 %s 下还有厂商", ErrRegistryConflict, name)
		}
		r.envs = append(r.envs[:i], r.envs[i+1:]...)
		return r.saveLocked()
	}
	return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, name)
}

func (r *Registry) AddEnterprise(envName, name, short string) error {
	if err := validateEnterprise(name, short); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	if findEntLocked(env, short) != nil {
		return fmt.Errorf("%w: 厂商简称 %s 已存在", ErrRegistryConflict, short)
	}
	env.Enterprises = append(env.Enterprises, Enterprise{Name: name, ShortName: short})
	return r.saveLocked()
}

func (r *Registry) UpdateEnterpriseName(envName, short, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("厂商名称必填")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	ent := findEntLocked(env, short)
	if ent == nil {
		return fmt.Errorf("%w: 厂商 %s", ErrRegistryNotFound, short)
	}
	ent.Name = name
	return r.saveLocked()
}

func (r *Registry) DeleteEnterprise(envName, short string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	for i := range env.Enterprises {
		if env.Enterprises[i].ShortName != short {
			continue
		}
		if len(env.Enterprises[i].DeviceTypes) > 0 {
			return fmt.Errorf("%w: 厂商 %s 下还有设备类型", ErrRegistryConflict, short)
		}
		env.Enterprises = append(env.Enterprises[:i], env.Enterprises[i+1:]...)
		return r.saveLocked()
	}
	return fmt.Errorf("%w: 厂商 %s", ErrRegistryNotFound, short)
}

func (r *Registry) AddDeviceType(envName, entShort, name, short string) error {
	if err := validateDeviceType(name, short); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	ent := findEntLocked(env, entShort)
	if ent == nil {
		return fmt.Errorf("%w: 厂商 %s", ErrRegistryNotFound, entShort)
	}
	if findTypeLocked(ent, short) != nil {
		return fmt.Errorf("%w: 类型简称 %s 已存在", ErrRegistryConflict, short)
	}
	ent.DeviceTypes = append(ent.DeviceTypes, DeviceType{Name: name, ShortName: short})
	return r.saveLocked()
}

func (r *Registry) UpdateDeviceTypeName(envName, entShort, short, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("类型名称必填")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	ent := findEntLocked(env, entShort)
	if ent == nil {
		return fmt.Errorf("%w: 厂商 %s", ErrRegistryNotFound, entShort)
	}
	dt := findTypeLocked(ent, short)
	if dt == nil {
		return fmt.Errorf("%w: 类型 %s", ErrRegistryNotFound, short)
	}
	dt.Name = name
	return r.saveLocked()
}

func (r *Registry) DeleteDeviceType(envName, entShort, short string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	ent := findEntLocked(env, entShort)
	if ent == nil {
		return fmt.Errorf("%w: 厂商 %s", ErrRegistryNotFound, entShort)
	}
	for i := range ent.DeviceTypes {
		if ent.DeviceTypes[i].ShortName != short {
			continue
		}
		ent.DeviceTypes = append(ent.DeviceTypes[:i], ent.DeviceTypes[i+1:]...)
		return r.saveLocked()
	}
	return fmt.Errorf("%w: 类型 %s", ErrRegistryNotFound, short)
}

// Resolve 校验 (环境, 厂商简称, 类型简称) 三级引用，并返回代入占位符后的 url。
func (r *Registry) Resolve(envName, entShort, typeShort, deviceID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.findEnvLocked(envName)
	if env == nil {
		return "", fmt.Errorf("%w: 环境 %s", ErrRegistryNotFound, envName)
	}
	ent := findEntLocked(env, entShort)
	if ent == nil {
		return "", fmt.Errorf("%w: 环境 %s 下无厂商 %s", ErrRegistryNotFound, envName, entShort)
	}
	if findTypeLocked(ent, typeShort) == nil {
		return "", fmt.Errorf("%w: 厂商 %s 下无类型 %s", ErrRegistryNotFound, entShort, typeShort)
	}
	return SubstituteURL(env.URL, entShort, typeShort, deviceID), nil
}
