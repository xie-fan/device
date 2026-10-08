//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func detachCmd(cmd *exec.Cmd) {
	// 新会话：脱离终端，simctl 退出或终端关掉后 manager 继续活。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
