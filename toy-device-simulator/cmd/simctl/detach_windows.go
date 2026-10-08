package main

import (
	"os/exec"
	"syscall"
)

func detachCmd(cmd *exec.Cmd) {
	// CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW：simctl 退出后 manager 继续活，不弹控制台。
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000200 | 0x08000000,
	}
}
