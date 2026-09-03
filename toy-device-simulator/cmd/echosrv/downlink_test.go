package main

import (
	"bytes"
	"testing"
)

// splitAMR 的合同：每个物理包都能独立当成一个 AMR 文件解析——包首有存储头、
// 包体是整数个帧、拼回去与原始帧流一致。固件按整包解析，切断帧就播不出来。
func TestSplitAMRPacketsAreSelfContained(t *testing.T) {
	// 3 个 AMR-WB mode 8 帧（每帧 61 字节，含 ToC）。
	frame := append([]byte{8 << 3}, bytes.Repeat([]byte{0xAB}, 60)...)
	stream := append(append([]byte(nil), amrWBHeader...), bytes.Repeat(frame, 3)...)

	packets := splitAMR(stream, 16000)
	if len(packets) == 0 {
		t.Fatal("splitAMR 返回空")
	}
	var body []byte
	for i, p := range packets {
		if !bytes.HasPrefix(p, amrWBHeader) {
			t.Fatalf("包 %d 缺 #!AMR-WB 存储头：% x", i, p[:min(9, len(p))])
		}
		rest := p[len(amrWBHeader):]
		if len(rest)%len(frame) != 0 {
			t.Fatalf("包 %d 切断了帧：正文 %d 字节不是帧长 %d 的整数倍", i, len(rest), len(frame))
		}
		body = append(body, rest...)
	}
	if !bytes.Equal(body, bytes.Repeat(frame, 3)) {
		t.Fatalf("拼回的帧流与原始不一致：%d vs %d 字节", len(body), 3*len(frame))
	}
}

// 坏帧（保留 mode）之后的残余必须丢掉，不能当成数据发出去。
func TestSplitAMRStopsAtBadFrame(t *testing.T) {
	frame := append([]byte{8 << 3}, bytes.Repeat([]byte{0xAB}, 60)...)
	stream := append(append([]byte(nil), amrWBHeader...), frame...)
	stream = append(stream, 11<<3, 0x00) // mode 11 = 保留

	packets := splitAMR(stream, 16000)
	if len(packets) != 1 || len(packets[0]) != len(amrWBHeader)+len(frame) {
		t.Fatalf("坏帧后应只剩 1 个完整包，得到 %d 包", len(packets))
	}
}

// splitMP3 的合同：包边界永远落在 MPEG 帧边界上。
func TestSplitMP3IsFrameAligned(t *testing.T) {
	// MPEG2 Layer III、16 kHz、64 kbps、无 padding → 72*64000/16000 = 288 字节。
	head := []byte{0xFF, 0xF2, 0x88, 0xC0} // bitrate idx 8 = 64 kbps，sr idx 2 = 16 kHz
	if got := mp3FrameLen(head); got != 288 {
		t.Fatalf("mp3FrameLen = %d，想要 288", got)
	}
	frame := append(head, bytes.Repeat([]byte{0x11}, 288-4)...)
	n := maxPacketBytes/288 + 5 // 保证切出多于一个包
	packets := splitMP3(bytes.Repeat(frame, n))
	if len(packets) < 2 {
		t.Fatalf("应切出多个包，得到 %d", len(packets))
	}
	for i, p := range packets {
		if len(p)%288 != 0 {
			t.Fatalf("包 %d 切断了帧：%d 字节不是 288 的整数倍", i, len(p))
		}
		if len(p) > maxPacketBytes {
			t.Fatalf("包 %d 超出物理上限：%d > %d", i, len(p), maxPacketBytes)
		}
	}
}

// 非帧数据（如残留的 ID3 标签）不该被当成音频发出去。
func TestSplitMP3RejectsNonFrameBytes(t *testing.T) {
	if got := splitMP3([]byte("ID3\x04\x00\x00\x00\x00")); got != nil {
		t.Fatalf("非帧数据应返回空，得到 %d 包", len(got))
	}
}

// aac 与 pcm 走字节硬切：每包不超上限，拼回去与原始一致。
func TestSplitBytesHardCut(t *testing.T) {
	src := bytes.Repeat([]byte{0x7F}, maxPacketBytes*2+123)
	packets := splitBytes(src, maxPacketBytes)
	if len(packets) != 3 {
		t.Fatalf("应切出 3 包，得到 %d", len(packets))
	}
	var joined []byte
	for _, p := range packets {
		if len(p) > maxPacketBytes {
			t.Fatalf("包超出上限：%d", len(p))
		}
		joined = append(joined, p...)
	}
	if !bytes.Equal(joined, src) {
		t.Fatal("拼回的字节与原始不一致")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
