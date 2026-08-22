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
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --config")
		return 1
	}
	if _, err := config.LoadFile(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "配置拒绝:", err)
		return 1
	}
	return 0
}
