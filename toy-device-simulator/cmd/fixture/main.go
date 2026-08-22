package main

import (
	"flag"
	"fmt"
	"os"

	"toy-device-simulator/internal/fixture"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 || args[0] != "alloc-fresh-id" {
		fmt.Fprintln(os.Stderr, "用法: fixture alloc-fresh-id --run-id <uuid> --out <path>")
		return 1
	}
	fs := flag.NewFlagSet("alloc-fresh-id", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runID := fs.String("run-id", "", "本次运行 UUID")
	out := fs.String("out", "", "fresh_ids.jsonl 路径")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if *runID == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --run-id 与 --out")
		return 1
	}
	existing, err := fixture.ReadJSONL(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取夹具失败:", err)
		return 1
	}
	line := fixture.Alloc(*runID, existing)
	if err := fixture.AppendJSONL(*out, line); err != nil {
		fmt.Fprintln(os.Stderr, "写入夹具失败:", err)
		return 1
	}
	fmt.Println(line.DeviceID)
	return 0
}
