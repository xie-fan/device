package api

import (
	"bytes"
	"testing"
)

// 下行 AMR 每包自带存储头，原样追加落盘后文件里有多个头。回放解码前必须只留
// 第一个——ffmpeg 会把中间的头当成 mode 4 的坏帧吃掉（实测 20s 音频被读成 20.68s）。
func TestStripRepeatedAMRHeaders(t *testing.T) {
	hdr := []byte("#!AMR-WB\n")
	frame := append([]byte{8 << 3}, bytes.Repeat([]byte{0xAB}, 60)...)
	three := bytes.Join([][]byte{
		append(append([]byte(nil), hdr...), frame...),
		append(append([]byte(nil), hdr...), frame...),
		append(append([]byte(nil), hdr...), frame...),
	}, nil)

	got := stripRepeatedAMRHeaders(three)
	if n := bytes.Count(got, hdr); n != 1 {
		t.Fatalf("应只剩 1 个存储头，得到 %d", n)
	}
	if !bytes.HasPrefix(got, hdr) {
		t.Fatal("首个存储头不能被删掉")
	}
	if want := append(append([]byte(nil), hdr...), bytes.Repeat(frame, 3)...); !bytes.Equal(got, want) {
		t.Fatalf("音频帧被改动：%d 字节 vs 期望 %d", len(got), len(want))
	}
}

// 只有一个头（单包下行）与非 AMR 数据都必须原样返回。
func TestStripRepeatedAMRHeadersLeavesOthersAlone(t *testing.T) {
	single := append([]byte("#!AMR-WB\n"), bytes.Repeat([]byte{0x44}, 61)...)
	if got := stripRepeatedAMRHeaders(single); !bytes.Equal(got, single) {
		t.Fatal("单头输入不该被改动")
	}
	notAMR := []byte{0xFF, 0xF1, 0x60, 0x40}
	if got := stripRepeatedAMRHeaders(notAMR); !bytes.Equal(got, notAMR) {
		t.Fatal("非 AMR 输入不该被改动")
	}
	nb := append([]byte("#!AMR\n"), bytes.Repeat([]byte{0x04}, 12)...)
	if got := stripRepeatedAMRHeaders(append(append([]byte(nil), nb...), nb...)); bytes.Count(got, []byte("#!AMR\n")) != 1 {
		t.Fatal("窄带 #!AMR 头同样要去重")
	}
}
