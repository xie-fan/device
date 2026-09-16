package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"toy-device-simulator/config"
)

type productsFile struct {
	Products []config.Product `yaml:"products"`
}

// Products 持锁维护产品清单，每次变更整份原子落盘（同 Registry.saveLocked）。
type Products struct {
	mu    sync.Mutex
	path  string
	items map[string]config.Product
}

func LoadProducts(path string) (*Products, error) {
	p := &Products{path: path, items: map[string]config.Product{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			def := config.DefaultProduct()
			p.items[def.ID] = def
			if err := p.saveLocked(); err != nil {
				return nil, err
			}
			return p, nil
		}
		return nil, err
	}
	var f productsFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("products 解析失败: %w", err)
	}
	for i, prod := range f.Products {
		if err := config.ValidateProduct(prod); err != nil {
			id := prod.ID
			if id == "" {
				id = fmt.Sprintf("#%d", i)
			}
			return nil, fmt.Errorf("products %s: %w", id, err)
		}
		if _, dup := p.items[prod.ID]; dup {
			return nil, fmt.Errorf("products 重复 id %s", prod.ID)
		}
		p.items[prod.ID] = prod
	}
	return p, nil
}

func (p *Products) saveLocked() error {
	ids := make([]string, 0, len(p.items))
	for id := range p.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	f := productsFile{Products: make([]config.Product, 0, len(ids))}
	for _, id := range ids {
		f.Products = append(f.Products, p.items[id])
	}
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(p.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (p *Products) List() []config.Product {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.items))
	for id := range p.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]config.Product, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.items[id])
	}
	return out
}

func (p *Products) Get(id string) (config.Product, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prod, ok := p.items[id]
	return prod, ok
}

func (p *Products) Add(prod config.Product) error {
	if err := config.ValidateProduct(prod); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.items[prod.ID]; ok {
		return fmt.Errorf("%w: 产品 %s 已存在", ErrRegistryConflict, prod.ID)
	}
	p.items[prod.ID] = prod
	return p.saveLocked()
}

func (p *Products) Update(id string, prod config.Product) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.items[id]; !ok {
		return fmt.Errorf("%w: 产品 %s", ErrRegistryNotFound, id)
	}
	if prod.ID == "" {
		prod.ID = id
	} else if prod.ID != id {
		return fmt.Errorf("id 与路径不符")
	}
	if err := config.ValidateProduct(prod); err != nil {
		return err
	}
	p.items[id] = prod
	return p.saveLocked()
}

func (p *Products) Delete(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.items[id]; !ok {
		return fmt.Errorf("%w: 产品 %s", ErrRegistryNotFound, id)
	}
	delete(p.items, id)
	return p.saveLocked()
}
