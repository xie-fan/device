package main

import "testing"

func TestVerdictTerminationTable(t *testing.T) {
	// 11 行终止表全覆盖，外加 down_bytes==0 与老录音回落。
	cases := []struct {
		name, end, kind, downFmt string
		bytes                    int
		want                     string
	}{
		{"仅 TTS idle", "idle", "tts", "pcm", 320, "replied"},
		{"仅 command", "idle", "command", "", 0, "replied_no_audio"},
		{"仅成功 JSON", "idle", "json", "", 0, "replied_no_audio"},
		{"command+TTS", "idle", "command+tts", "mp3", 100, "replied"},
		{"JSON+TTS", "idle", "json+tts", "pcm", 80, "replied"},
		{"仅 IsFinal 无终态", "idle", "silent", "", 0, "silent"},
		{"仅 interim 或全无", "timeout", "", "", 0, "no_reply"},
		{"timeout 且 drop", "timeout", "", "", 0, "no_reply"},
		{"失败 JSON", "error", "", "", 0, "error"},
		{"interrupt 保持 tts", "interrupt", "tts", "pcm", 50, "aborted"},
		{"interrupt 空", "interrupt", "", "", 0, "aborted"},
		{"连接收口 Phase C", "connection_lost", "tts", "pcm", 50, "aborted"},
		{"connection_lost 空", "connection_lost", "", "", 0, "aborted"},
		{"含 tts 但 0 字节", "idle", "tts", "", 0, "silent"},
		{"command+tts 但 0 字节", "idle", "command+tts", "", 0, "silent"},
		{"老录音 tts 无 down_bytes", "idle", "tts", "mp3", 0, "replied"},
		{"老录音 command+tts 无 down_bytes", "idle", "command+tts", "pcm", 0, "replied"},
	}
	for _, c := range cases {
		got := Verdict(c.end, c.kind, c.downFmt, c.bytes)
		if got != c.want {
			t.Fatalf("%s: Verdict(%q,%q,%q,%d)=%q want %q",
				c.name, c.end, c.kind, c.downFmt, c.bytes, got, c.want)
		}
	}
}
