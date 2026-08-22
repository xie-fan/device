package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("speak", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "设备 YAML 路径")
	audioPath := fs.String("audio", "", "上行 WAV 路径")
	wait := fs.Bool("wait", false, "等待 turn_terminal")
	inject := fs.String("inject", "", "fault 注入")
	deviceID := fs.String("device-id", "", "覆盖 YAML 的 device_id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *cfgPath == "" || *audioPath == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --config 与 --audio")
		return 1
	}

	cfg, err := config.LoadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置拒绝:", err)
		return 1
	}
	if *deviceID != "" {
		cfg.DeviceID = *deviceID
	}
	fault, err := core.ParseFault(*inject)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	d := core.NewDevice(cfg, core.Options{Fault: fault})
	defer d.Shutdown()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		d.RequestFinalize("user_stop", true)
	}()

	if err := d.Start(0); err != nil {
		fmt.Fprintln(os.Stderr, "连接失败:", err)
		return 1
	}

	wav, err := os.ReadFile(*audioPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 WAV 失败:", err)
		return 1
	}
	pcm, err := core.DecodeWAV(wav)
	if err != nil {
		fmt.Fprintln(os.Stderr, "WAV 非法:", err)
		return 1
	}
	if err := pcm.Match(cfg.Audio.SampleRate, cfg.Audio.Channels, cfg.Audio.SampleFormat); err != nil {
		fmt.Fprintln(os.Stderr, "WAV 与设备 audio_* 不符:", err)
		return 1
	}

	if _, _, err := d.Speak(pcm.Samples); err != nil {
		fmt.Fprintln(os.Stderr, "speak 失败:", err)
		return 1
	}

	if !*wait {
		return 0
	}

	budget := d.WaitBudgetFor(len(pcm.Samples))
	ev, err := d.WaitTurn(budget)
	if err != nil {
		fmt.Fprintln(os.Stderr, "等待 Turn 失败:", err)
		if errors.Is(err, core.ErrWaitTimeout) {
			return 2
		}
		return 1
	}
	b, _ := json.Marshal(ev)
	fmt.Println(string(b))
	if ev.EndReason != core.EndIdle {
		for _, e := range d.Events() {
			if e.Type == "protocol_error" && e.Reason != "" {
				fmt.Fprintln(os.Stderr, "protocol_error:", e.Reason)
			}
		}
	}
	return exitCode(ev, fault)
}

func exitCode(ev core.Event, fault core.Fault) int {
	switch ev.EndReason {
	case core.EndIdle:
		return 0
	case core.EndTimeout:
		if core.FaultExpectsDrop(fault) {
			return 0
		}
		return 2
	default:
		return 1
	}
}
