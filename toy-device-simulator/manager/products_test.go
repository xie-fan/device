package manager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"toy-device-simulator/config"
)

func mh8wProduct() config.Product {
	p := config.DefaultProduct()
	p.ID = "mh8w"
	p.Name = "MH8W 故事机"
	p.AudioFormats = []string{"amr/16000"}
	p.Defaults.Audio.Format = "amr"
	p.Defaults.Audio.SampleRate = 16000
	return p
}

func productIDs(ps []config.Product) []string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestLoadProductsMissingFileSeedsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "products.yaml")
	ps, err := LoadProducts(path)
	if err != nil {
		t.Fatalf("缺文件应自动生成默认产品: %v", err)
	}
	if got := productIDs(ps.List()); len(got) != 1 || got[0] != "default" {
		t.Fatalf("应只有 default，得到 %v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("默认产品应已落盘: %v", err)
	}
	if !strings.Contains(string(raw), "id: default") {
		t.Fatalf("落盘内容应含 id: default: %s", raw)
	}
	ps2, err := LoadProducts(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := productIDs(ps2.List()); len(got) != 1 {
		t.Fatalf("重载不应重复生成，得到 %v", got)
	}
	got, ok := ps2.Get("default")
	if !ok || config.ValidateProduct(got) != nil {
		t.Fatalf("重载回来的默认产品应仍能过校验: %+v", got)
	}
}

func TestLoadProductsCorruptOrInvalidFails(t *testing.T) {
	cases := []string{
		"products: [::",
		// 手改出一个非法产品：manager 起不来，同配置树。
		"products:\n  - id: bad\n    name: 坏产品\n    playing_modes: [9]\n    audio_formats: [pcm/16000]\n",
	}
	for i, raw := range cases {
		p := filepath.Join(t.TempDir(), "products.yaml")
		if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProducts(p); err == nil {
			t.Fatalf("case %d 应加载失败", i)
		}
	}
}

func TestProductsCRUDPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "products.yaml")
	ps, err := LoadProducts(path)
	if err != nil {
		t.Fatal(err)
	}
	p := mh8wProduct()
	if err := ps.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got, ok := ps.Get("mh8w"); !ok || got.Name != "MH8W 故事机" || got.Defaults.Audio.Format != "amr" {
		t.Fatalf("Get 不符: %+v %v", got, ok)
	}
	if err := ps.Add(p); !errors.Is(err, ErrRegistryConflict) {
		t.Fatalf("重复 id 应 ErrRegistryConflict，得到 %v", err)
	}
	bad := mh8wProduct()
	bad.ID = "bad"
	bad.Name = ""
	if err := ps.Add(bad); err == nil || errors.Is(err, ErrRegistryConflict) {
		t.Fatalf("校验失败应是普通错误，得到 %v", err)
	}
	if _, ok := ps.Get("bad"); ok {
		t.Fatal("校验失败的产品不得入库")
	}

	upd := mh8wProduct()
	upd.ID = "" // 省略 id = 沿用路径上的 id
	upd.Name = "MH8W 二代"
	if err := ps.Update("mh8w", upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	other := mh8wProduct()
	other.ID = "other"
	if err := ps.Update("mh8w", other); err == nil {
		t.Fatal("id 与路径不符应报错")
	}
	if err := ps.Update("nope", mh8wProduct()); !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("更新不存在的产品应 ErrRegistryNotFound，得到 %v", err)
	}
	if got := productIDs(ps.List()); len(got) != 2 || got[0] != "default" || got[1] != "mh8w" {
		t.Fatalf("List 应按 id 排序，得到 %v", got)
	}

	ps2, err := LoadProducts(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ps2.Get("mh8w"); !ok || got.Name != "MH8W 二代" || got.ID != "mh8w" {
		t.Fatalf("重载后应保留更新: %+v %v", got, ok)
	}
	if err := ps2.Delete("mh8w"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := ps2.Delete("mh8w"); !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("再删应 ErrRegistryNotFound，得到 %v", err)
	}
	ps3, err := LoadProducts(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := productIDs(ps3.List()); len(got) != 1 || got[0] != "default" {
		t.Fatalf("删除应落盘，得到 %v", got)
	}
}

func TestRegistryDefaultProduct(t *testing.T) {
	p := filepath.Join(t.TempDir(), "registry.yaml")
	r, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddEnvironment("测试", "ws://h/{enterprise}", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.AddEnterprise("测试", "威普爱", "vp"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddDeviceType("测试", "vp", "语音", "VT"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.DefaultProduct("测试", "vp", "VT"); err != nil || got != "" {
		t.Fatalf("新类型不应有默认产品，得到 %q %v", got, err)
	}
	if err := r.SetDeviceTypeDefaultProduct("测试", "vp", "VT", "mh8w"); err != nil {
		t.Fatalf("设默认产品: %v", err)
	}
	if err := r.SetDeviceTypeDefaultProduct("测试", "vp", "NOPE", "mh8w"); !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("类型不存在应 ErrRegistryNotFound，得到 %v", err)
	}
	if _, err := r.DefaultProduct("测试", "nope", "VT"); !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("厂商不存在应 ErrRegistryNotFound，得到 %v", err)
	}
	if refs := r.ProductReferences("mh8w"); len(refs) != 1 || refs[0] != "测试/vp/VT" {
		t.Fatalf("引用应为 [测试/vp/VT]，得到 %v", refs)
	}
	if refs := r.ProductReferences("other"); len(refs) != 0 {
		t.Fatalf("没被引用的产品应无引用，得到 %v", refs)
	}
	// 改类型名称与简称不丢默认产品。
	if err := r.UpdateDeviceType("测试", "vp", "VT", "语音二代", "VT2"); err != nil {
		t.Fatal(err)
	}

	r2, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r2.DefaultProduct("测试", "vp", "VT2"); err != nil || got != "mh8w" {
		t.Fatalf("重载后默认产品应保留，得到 %q %v", got, err)
	}
	if err := r2.SetDeviceTypeDefaultProduct("测试", "vp", "VT2", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "default_product") {
		t.Fatalf("清空后不应再写 default_product: %s", raw)
	}
	if refs := r2.ProductReferences("mh8w"); len(refs) != 0 {
		t.Fatalf("清空后应无引用，得到 %v", refs)
	}
}
