package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"toy-device-simulator/api"
	"toy-device-simulator/manager"
	"toy-device-simulator/media"
)

func main() {
	fs := flag.NewFlagSet("manager", flag.ExitOnError)
	cfgPath := fs.String("config", "", "Manager YAML 路径")
	addr := fs.String("listen", "127.0.0.1:8090", "HTTP 监听地址")
	templates := fs.String("templates", filepath.Join("configs", "templates"), "模板目录")
	recordings := fs.String("recordings", "recordings", "录音根目录")
	registry := fs.String("registry", filepath.Join("configs", "registry.yaml"), "配置树 YAML 路径")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --config")
		os.Exit(1)
	}
	cfg, err := manager.LoadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置拒绝:", err)
		os.Exit(1)
	}
	// ffmpeg 缺失只降级不拦截：pcm/wav 全路径不依赖它。
	tc, err := media.Detect(cfg.FFmpegPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ffmpeg 不可用（多格式音频功能受限）：%v\n", err)
		tc = nil
	} else {
		fmt.Fprintf(os.Stderr, "ffmpeg: %s\n编码能力: %s\n", tc.FFmpeg, tc.Capabilities())
	}
	h, err := api.New(api.Options{
		Config:        cfg,
		TemplatesDir:  *templates,
		RecordingsDir: *recordings,
		RegistryPath:  *registry,
		Media:         tc,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry 拒绝:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "manager listening on %s\n", *addr)
	if err := http.ListenAndServe(*addr, h); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
