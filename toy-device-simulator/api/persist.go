package api

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWrite 先写同目录的 .tmp 再 rename。直接覆写目标文件的话，进程在写到
// 一半时崩溃会留下半截 YAML/JSON，下次启动加载失败——而这两份索引正是
// 「设备还在不在」「库里有什么」的唯一真相源。
//
// 与 manager/registry.go 的落盘同一套做法（那边跨包用不上这个 helper）。
// Windows 上 os.Rename 覆盖已存在文件是 MoveFileEx 语义，可用。
func atomicWrite(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // 别留垃圾，否则下次启动会看见一个没人认的 .tmp
		return err
	}
	return nil
}

// persistWarn 落盘失败不回滚内存——改已经生效了，回滚只会让状态更乱。
// 但必须喊出来：静默失败的表现是「manager 重启后设备/资产凭空消失」，
// 而磁盘满、权限变化、杀毒软件短暂锁文件都会触发它。
func persistWarn(what string, err error) error {
	if err != nil {
		fmt.Fprintf(os.Stderr, "落盘失败 %s: %v\n", what, err)
	}
	return err
}

// persistErr 把落盘失败塞进 HTTP 响应。只记日志不够：agent 拿到 200
// 会当作已经落盘，而这两者分叉正是最难查的一类问题。
func persistErr(m map[string]any, err error) map[string]any {
	if err != nil {
		m["persist_error"] = err.Error()
	}
	return m
}
