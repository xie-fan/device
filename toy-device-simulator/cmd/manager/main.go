package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"toy-device-simulator/api"
	"toy-device-simulator/manager"
	"toy-device-simulator/media"
)

// isLoopbackListen 判断这个监听地址是不是只有本机看得见。
// 空 host（":8090"）与 0.0.0.0 都是「所有网卡」，算暴露。
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // 没写端口，整串当 host
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func main() {
	fs := flag.NewFlagSet("manager", flag.ExitOnError)
	cfgPath := fs.String("config", "", "Manager YAML 路径")
	addr := fs.String("listen", "127.0.0.1:8090", "HTTP 监听地址")
	templates := fs.String("templates", filepath.Join("configs", "templates"), "模板目录")
	recordings := fs.String("recordings", "recordings", "录音根目录")
	registry := fs.String("registry", filepath.Join("configs", "registry.yaml"), "配置树 YAML 路径")
	allowRemote := fs.Bool("allow-remote", false, "允许监听非 loopback 地址（这套 API 没有认证）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --config")
		os.Exit(1)
	}
	// 这套 HTTP API 没有认证，却能删设备、改定义、删历史、改配置树、注入故障、
	// 跑 scenario。默认只听 127.0.0.1 是安全的，但 --listen 0.0.0.0:8090 一条命令
	// 就能把整个设备实验室的控制权送出去，且不会有任何提示。护栏比认证便宜得多。
	if !isLoopbackListen(*addr) {
		if !*allowRemote {
			fmt.Fprintf(os.Stderr, "拒绝监听 %s：这套 API 无认证，能删设备、改定义、删历史、改配置树、注入故障、跑 scenario。\n确认要暴露给同网段的人，再加 --allow-remote。\n", *addr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "警告：无认证的控制面正暴露在 %s，同网段任何人都能删你的设备。\n", *addr)
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
