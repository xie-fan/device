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

func TestHintOnly(t *testing.T) {
	cases := []struct {
		verdict string
		chunks  int
		bytes   int
		want    bool
	}{
		{"replied", 1, 1900, true},    // 只有提示音
		{"replied", 12, 64000, false}, // 真回复
		{"replied", 1, 64000, false},  // 一大包，是真回复
		{"replied", 0, 1900, false},   // 读不到事件不判
		{"no_reply", 1, 1900, false},
	}
	for _, c := range cases {
		if got := hintOnly(c.verdict, c.chunks, c.bytes); got != c.want {
			t.Errorf("hintOnly(%+v)=%v", c, got)
		}
	}
}

func TestPhotoResult(t *testing.T) {
	cases := []struct {
		p       photoSummary
		kind    string
		verdict string
		hint    bool
		on      bool
		want    string
	}{
		{photoSummary{Command: true, Uploaded: true}, "command+tts", "replied", false, true, "ok"},
		{photoSummary{Command: true, Uploaded: true}, "command+tts", "replied", true, true, "no_reply"},
		{photoSummary{Command: true, Uploaded: true}, "command", "replied_no_audio", false, true, "no_reply"},
		{photoSummary{Command: true, Skipped: "没配图"}, "command", "replied_no_audio", false, true, "skipped:没配图"},
		{photoSummary{Command: true}, "command", "replied_no_audio", false, true, "not_uploaded"},
		{photoSummary{Uploaded: true}, "tts", "replied", false, false, "image_sent"},
		{photoSummary{}, "tts", "replied", false, true, "no_command"},
		{photoSummary{}, "tts", "replied", false, false, ""},
	}
	for _, c := range cases {
		if got := photoResult(c.p, c.kind, c.verdict, c.hint, c.on); got != c.want {
			t.Errorf("photoResult(%+v)=%q want %q", c, got, c.want)
		}
	}
}

func TestCompactEventsFoldsTTSChunks(t *testing.T) {
	ev := func(typ, turn string, n float64) map[string]any {
		return map[string]any{"event_type": typ, "turn_id": turn, "device_id": "d", "instance_id": "i", "payload_len": n}
	}
	got := compactEvents([]map[string]any{
		ev("vad", "t1", 0),
		ev("tts_chunk", "t1", 100), ev("tts_chunk", "t1", 200), ev("tts_chunk", "other", 999), ev("tts_chunk", "t1", 300),
		ev("command_received", "t1", 0),
		ev("tts_chunk", "t1", 50),
	}, "t1")
	if len(got) != 4 {
		t.Fatalf("应折成 vad / tts_chunk×3 / command_received / tts_chunk×1，得到 %v", got)
	}
	if got[1]["count"] != 3 || got[1]["payload_len"] != 600.0 || got[3]["count"] != 1 {
		t.Fatalf("折叠计数不对: %v", got)
	}
	if _, has := got[0]["device_id"]; has {
		t.Fatalf("顶层已有的 id 不该留在每条事件里: %v", got[0])
	}
}
