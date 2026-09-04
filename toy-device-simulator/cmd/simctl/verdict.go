package main

import "strings"

// Verdict 把终止表映射成六值判语。只分类协议事实，不断言成败。
// 老录音没有 down_bytes（读出来是 0）：含 tts 且 down_format 非空则当 replied。
func Verdict(endReason, replyKind, downFormat string, downBytes int) string {
	switch endReason {
	case "timeout":
		return "no_reply"
	case "error":
		return "error"
	case "interrupt", "connection_lost":
		return "aborted"
	}
	if endReason != "idle" {
		return "error"
	}
	switch replyKind {
	case "command", "json":
		return "replied_no_audio"
	case "silent":
		return "silent"
	}
	if strings.Contains(replyKind, "tts") {
		if downBytes > 0 {
			return "replied"
		}
		if downFormat != "" { // 老录音回落
			return "replied"
		}
		return "silent"
	}
	return "silent"
}
