package main

import (
	"flag"
	"fmt"
	"os"

	"toy-device-simulator/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "设备 YAML 路径")
	allowProduction := fs.Bool("allow-production", false, "允许非 loopback 的 server.url")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --config")
		return 1
	}
	d, err := config.LoadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置拒绝:", err)
		return 1
	}
	// 非 loopback 默认拒绝；MH 已由 Validate 硬拒绝，本 flag 不能放行 MH。
	if !config.IsLoopbackServerURL(d.Server.URL) {
		fmt.Fprintln(os.Stderr, "警告: server.url 主机不是 loopback（localhost / 127.0.0.1 / ::1）")
		if !*allowProduction {
			return 1
		}
	}
	return 0
}
