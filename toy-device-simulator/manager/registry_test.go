package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistryMissingFileIsEmpty(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatalf("缺文件应得空树: %v", err)
	}
	if len(r.Snapshot()) != 0 {
		t.Fatal("空树应无环境")
	}
}

func TestLoadRegistryCorruptFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(p, []byte("environments: [::"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(p); err == nil {
		t.Fatal("损坏 YAML 应加载失败")
	}
}

func TestLoadRegistryRejectsHandEditedBadTree(t *testing.T) {
	cases := []string{
		// 重复环境名
		"environments:\n  - name: a\n    url: ws://h/\n  - name: a\n    url: ws://h/\n",
		// 未知占位符
		"environments:\n  - name: a\n    url: ws://h/{vendor}\n",
	}
	for i, raw := range cases {
		p := filepath.Join(t.TempDir(), "r.yaml")
		if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRegistry(p); err == nil {
			t.Fatalf("case %d 手改坏树应加载失败", i)
		}
	}
}

func TestValidateEnvURL(t *testing.T) {
	ok := []string{
		"ws://127.0.0.1:8089/",
		"wss://h.example.com/{enterprise}",
		"ws://h/{enterprise}/{device_type}/{device_id}",
	}
	for _, u := range ok {
		if err := ValidateEnvURL(u); err != nil {
			t.Fatalf("%s 应合法: %v", u, err)
		}
	}
	bad := []string{"", "http://h/", "ws://h/{vendor}", "ws:///nohost"}
	for _, u := range bad {
		if err := ValidateEnvURL(u); err == nil {
			t.Fatalf("%s 应拒绝", u)
		}
	}
}

func TestSubstituteURL(t *testing.T) {
	got := SubstituteURL("ws://h/{enterprise}/{device_type}/{device_id}", "vp", "VT", "sim_1")
	if got != "ws://h/vp/VT/sim_1" {
		t.Fatalf("代入结果不符: %s", got)
	}
	if SubstituteURL("ws://h/fixed", "a", "b", "c") != "ws://h/fixed" {
		t.Fatal("无占位符应原样返回")
	}
}

func TestRegistrySaveAndReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "registry.yaml")
	r, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AddEnvironment("测试", "ws://h/{enterprise}"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddEnterprise("测试", "威普爱", "vp"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddDeviceType("测试", "vp", "语音", "VT"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("应已落盘: %v", err)
	}
	for _, want := range []string{"测试", "vp", "VT", "short_name"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("落盘应含 %s: %s", want, raw)
		}
	}
	r2, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	url, err := r2.Resolve("测试", "vp", "VT", "sim_9")
	if err != nil {
		t.Fatalf("重载后 Resolve 失败: %v", err)
	}
	if url != "ws://h/vp" {
		t.Fatalf("Resolve 代入不符: %s", url)
	}
}
