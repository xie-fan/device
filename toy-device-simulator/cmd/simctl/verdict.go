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

// hintOnly：判语 replied，但下行只有一包且小得像断句提示音（A3.amr 约 1900 字节），
// 也就是其实没回答。chunks 是本轮 tts_chunk 事件数；读不到事件（0）不判。
// ponytail: 按包数+字节猜，真回复恰好只有一小包会误判；要准得让 core 把提示音包单独记事件。
func hintOnly(verdict string, chunks, downBytes int) bool {
	return verdict == "replied" && chunks == 1 && downBytes <= 4096
}

// photoResult 把拍照三项事实归成一个结论，agent 不用再查 photo.md 的对照表。
// photoOn 是设备当前配置的 features.photo.enabled；空串表示这轮与拍照无关。
func photoResult(p photoSummary, replyKind, verdict string, hint, photoOn bool) string {
	switch {
	case p.Skipped != "":
		return "skipped:" + p.Skipped // 设备没传图：功能没开 / 没配图 / 读图失败 / 组帧失败
	case p.Command && p.Uploaded && strings.Contains(replyKind, "tts") && verdict == "replied" && !hint:
		return "ok"
	case p.Command && p.Uploaded:
		return "no_reply" // 图传了，等识图回复超时（features.photo.reply_timeout_sec）
	case p.Command:
		return "not_uploaded"
	case p.Uploaded:
		return "image_sent" // --image 带图送话发出了；服务端用没用看 imageChat
	case photoOn:
		return "no_command" // 开了拍照但服务端没下发 601：问句没交给 Camera
	}
	return ""
}
