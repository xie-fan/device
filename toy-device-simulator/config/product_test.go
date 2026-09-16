package config

import "testing"

func TestDefaultProductIsValid(t *testing.T) {
	p := DefaultProduct()
	if p.ID != "default" {
		t.Fatalf("默认产品 id 应为 default，得到 %q", p.ID)
	}
	if err := ValidateProduct(p); err != nil {
		t.Fatalf("默认产品必须能过校验: %v", err)
	}
	d := p.Defaults
	if d.Audio.Format != "pcm" || d.Audio.SampleRate != 16000 || d.PlayingMode != 1 || d.NicType != "wifi" {
		t.Fatalf("默认产品应是 wifi、pcm/16000、按键：%+v", d)
	}
	if d.DeviceID != "" || d.Enterprise != "" || d.DeviceType != "" || d.Server.URL != "" {
		t.Fatalf("产品默认值不得带 device_id 或挂靠：%+v", d)
	}
	if d.Features.Photo.Enabled {
		t.Fatal("默认产品不开拍照")
	}
}

func amrProduct() Product {
	p := DefaultProduct()
	p.ID = "mh8w"
	p.Name = "MH8W"
	p.PlayingModes = []int{1, 3}
	p.AudioFormats = []string{"amr/16000", "amr/8000"}
	p.Defaults.Audio.Format = "amr"
	p.Defaults.Audio.SampleRate = 16000
	return p
}

func TestValidateProductAcceptsDefaultsInsideLists(t *testing.T) {
	p := amrProduct()
	if err := ValidateProduct(p); err != nil {
		t.Fatalf("合法产品被拒: %v", err)
	}
	// 开了拍照但没配图也合法：收到指令时只记事件，不传图。
	p.Defaults.Features.Photo.Enabled = true
	if err := ValidateProduct(p); err != nil {
		t.Fatalf("开拍照不配图应合法: %v", err)
	}
}

func TestValidateProductRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(p *Product)
	}{
		{"空 id", func(p *Product) { p.ID = "" }},
		{"id 带路径分隔符", func(p *Product) { p.ID = "a/b" }},
		{"空名称", func(p *Product) { p.Name = "  " }},
		{"对话模式清单为空", func(p *Product) { p.PlayingModes = nil }},
		{"对话模式越界", func(p *Product) { p.PlayingModes = []int{1, 4} }},
		{"对话模式重复", func(p *Product) { p.PlayingModes = []int{1, 1} }},
		{"音频格式清单为空", func(p *Product) { p.AudioFormats = nil }},
		{"音频格式缺采样率", func(p *Product) { p.AudioFormats = []string{"amr"} }},
		{"采样率不是数字", func(p *Product) { p.AudioFormats = []string{"amr/abc"} }},
		{"不支持的格式", func(p *Product) { p.AudioFormats = []string{"flac/16000"} }},
		{"amr 采样率非法", func(p *Product) { p.AudioFormats = []string{"amr/16000", "amr/44100"} }},
		{"音频格式重复", func(p *Product) { p.AudioFormats = []string{"amr/16000", "amr/16000"} }},
		{"默认对话模式不在清单内", func(p *Product) { p.Defaults.PlayingMode = 2 }},
		{"默认音频格式不在清单内", func(p *Product) { p.AudioFormats = []string{"amr/8000"} }},
		{"默认值带 device_id", func(p *Product) { p.Defaults.DeviceID = "x" }},
		{"默认值带 enterprise", func(p *Product) { p.Defaults.Enterprise = "x" }},
		{"默认值带 device_type", func(p *Product) { p.Defaults.DeviceType = "x" }},
		{"默认值带 server.url", func(p *Product) { p.Defaults.Server.URL = "ws://127.0.0.1:1/" }},
		{"默认值属性非法", func(p *Product) { p.Defaults.Audio.Channels = 2 }},
		{"拍照分片间隔为负", func(p *Product) { p.Defaults.Features.Photo.SliceIntervalMs = -1 }},
		{"拍照回复超时为负", func(p *Product) { p.Defaults.Features.Photo.ReplyTimeoutSec = -1 }},
	}
	for _, c := range cases {
		p := amrProduct()
		c.mut(&p)
		if err := ValidateProduct(p); err == nil {
			t.Errorf("%s 应被拒", c.name)
		}
	}
}

func TestParseAudioSpec(t *testing.T) {
	f, sr, err := ParseAudioSpec("amr/16000")
	if err != nil || f != "amr" || sr != 16000 {
		t.Fatalf("amr/16000 应解析为 amr 16000，得到 %q %d %v", f, sr, err)
	}
	for _, bad := range []string{"", "amr", "amr/", "/16000", "amr/0", "amr/-8000", "amr/x", "flac/16000", "amr/16000/1"} {
		if _, _, err := ParseAudioSpec(bad); err == nil {
			t.Errorf("%q 应解析失败", bad)
		}
	}
}
