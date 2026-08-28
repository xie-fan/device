package media

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNearestRateAndDefaults(t *testing.T) {
	cases := []struct {
		format string
		sr     int
		in     float64
		want   float64
	}{
		{FormatMP3, 44100, 0, 128},
		{FormatMP3, 44100, 192, 192},
		{FormatAAC, 16000, 0, 96},
		{FormatAMR, 8000, 0, 12.2},
		{FormatAMR, 8000, 13, 12.2},
		{FormatAMR, 8000, 5, 5.15},
		{FormatAMR, 8000, 1, 4.75},
		{FormatAMR, 8000, 99, 12.2},
		{FormatAMR, 16000, 0, 23.85},
		{FormatAMR, 16000, 12, 12.65},
		{FormatPCM, 16000, 128, 0},
		{FormatWAV, 16000, 128, 0},
	}
	for _, c := range cases {
		if got := NormalizeBitrate(c.format, c.sr, c.in); got != c.want {
			t.Errorf("NormalizeBitrate(%s,%d,%g)=%g want %g", c.format, c.sr, c.in, got, c.want)
		}
	}
}

func TestEncodeArgsAMRSampleRateConstraint(t *testing.T) {
	if _, _, err := encodeArgs(Spec{Format: FormatAMR, SampleRate: 16000}); err != nil {
		t.Fatalf("amr 16000 应合法：%v", err)
	}
	if _, _, err := encodeArgs(Spec{Format: FormatAMR, SampleRate: 44100}); err == nil {
		t.Fatal("amr 44100 应报错")
	}
	if _, _, err := encodeArgs(Spec{Format: "flac", SampleRate: 44100}); err == nil {
		t.Fatal("未知目标格式应报错")
	}
}

func TestParseEncoders(t *testing.T) {
	out := `Encoders:
 V..... = Video
 A..... = Audio
 ------
 V....D libx264              H.264
 A....D libmp3lame           MP3 (MPEG audio layer 3) (codec mp3)
 A....D aac                  AAC (Advanced Audio Coding)
`
	enc := parseEncoders(out)
	if !enc["libmp3lame"] || !enc["aac"] {
		t.Fatalf("应识别 libmp3lame/aac：%v", enc)
	}
	if enc["libx264"] {
		t.Fatal("视频编码器不应收录")
	}
}

func TestNormalizeFormat(t *testing.T) {
	cases := []struct{ fn, codec, want string }{
		{"wav", "pcm_s16le", FormatWAV},
		{"mp3", "mp3", FormatMP3},
		{"amr", "amr_nb", FormatAMR},
		{"aac", "aac", FormatAAC},
		{"mov,mp4,m4a,3gp,3g2,mj2", "aac", "mov"},
		{"s16le", "pcm_s16le", FormatPCM},
	}
	for _, c := range cases {
		if got := normalizeFormat(c.fn, c.codec); got != c.want {
			t.Errorf("normalizeFormat(%q,%q)=%q want %q", c.fn, c.codec, got, c.want)
		}
	}
}

// requireTC 无 ffmpeg 环境跳过（CI 不强依赖）。
func requireTC(t *testing.T) *Toolchain {
	t.Helper()
	tc, err := Detect("")
	if err != nil {
		t.Skipf("跳过（无 ffmpeg）：%v", err)
	}
	return tc
}

// writeTestWAV 生成 440Hz 单声道 16k s16le 正弦波 WAV。
func writeTestWAV(t *testing.T, ms int) string {
	t.Helper()
	const sr = 16000
	n := sr * ms / 1000
	data := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/sr))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
	}
	buf := make([]byte, 0, 44+len(data))
	u32 := func(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
	u16 := func(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
	buf = append(buf, "RIFF"...)
	buf = append(buf, u32(uint32(36+len(data)))...)
	buf = append(buf, "WAVEfmt "...)
	buf = append(buf, u32(16)...)
	buf = append(buf, u16(1)...)          // PCM
	buf = append(buf, u16(1)...)          // mono
	buf = append(buf, u32(sr)...)         // sample rate
	buf = append(buf, u32(sr*2)...)       // byte rate
	buf = append(buf, u16(2)...)          // block align
	buf = append(buf, u16(16)...)         // bits
	buf = append(buf, "data"...)
	buf = append(buf, u32(uint32(len(data)))...)
	buf = append(buf, data...)
	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeWAV(t *testing.T) {
	tc := requireTC(t)
	src := writeTestWAV(t, 1000)
	info, err := tc.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != FormatWAV || info.SampleRate != 16000 || info.Channels != 1 {
		t.Fatalf("探测结果异常：%+v", info)
	}
	if info.DurationMs < 950 || info.DurationMs > 1050 {
		t.Fatalf("时长应≈1000ms：%d", info.DurationMs)
	}
}

func TestTranscodeMP3RoundTrip(t *testing.T) {
	tc := requireTC(t)
	if err := tc.CanEncode(FormatMP3, 16000); err != nil {
		t.Skipf("跳过：%v", err)
	}
	ctx := context.Background()
	src := writeTestWAV(t, 1000)
	mp3Path := filepath.Join(t.TempDir(), "out.mp3")
	spec := Spec{Format: FormatMP3, SampleRate: 16000, Channels: 1, BitrateKbps: 64}
	if err := tc.TranscodeFile(ctx, src, mp3Path, spec); err != nil {
		t.Fatal(err)
	}
	info, err := tc.Probe(ctx, mp3Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != FormatMP3 || info.SampleRate != 16000 {
		t.Fatalf("mp3 探测异常：%+v", info)
	}
	if !info.Matches(spec) {
		t.Fatalf("转码结果应与 spec 一致：%+v", info)
	}
	if info.DurationMs < 900 || info.DurationMs > 1150 {
		t.Fatalf("mp3 时长应≈1000ms：%d", info.DurationMs)
	}
	// 转回裸 PCM：字节数应≈ 16000*2*1s。
	pcmPath := filepath.Join(t.TempDir(), "back.pcm")
	if err := tc.TranscodeFile(ctx, mp3Path, pcmPath, Spec{Format: FormatPCM, SampleRate: 16000, Channels: 1}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() < 28000 || st.Size() > 40000 {
		t.Fatalf("PCM 尺寸应≈32000：%d", st.Size())
	}
}

func TestStreamRealtimePacesOutput(t *testing.T) {
	tc := requireTC(t)
	if err := tc.CanEncode(FormatMP3, 16000); err != nil {
		t.Skipf("跳过：%v", err)
	}
	ctx := context.Background()
	src := writeTestWAV(t, 2000)
	mp3Path := filepath.Join(t.TempDir(), "pace.mp3")
	spec := Spec{Format: FormatMP3, SampleRate: 16000, Channels: 1, BitrateKbps: 64}
	if err := tc.TranscodeFile(ctx, src, mp3Path, spec); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(mp3Path)

	rc, err := tc.StreamRealtime(ctx, mp3Path, spec, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	start := time.Now()
	got, err := io.ReadAll(rc)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	// -c copy 直通：重新封装会重写 Xing/LAME 元数据头，允许 ±15% 差异。
	if len(got) < len(want)*85/100 || len(got) > len(want)*115/100 {
		t.Fatalf("直通字节数偏差过大：got=%d want=%d", len(got), len(want))
	}
	// -re 限速生效则秒级（实测 2s 音频 ≈1.6s，EOF 有 ~0.4s 固定提前）；
	// 不节流毫秒级读完。阈值取 1.2s 区分两者，不设上限（CI 容差）。
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("-re 未生效：%v", elapsed)
	}
}

func TestStreamRealtimeCancelKillsProcess(t *testing.T) {
	tc := requireTC(t)
	ctx := context.Background()
	src := writeTestWAV(t, 3000)
	rc, err := tc.StreamRealtime(ctx, src, Spec{Format: FormatPCM, SampleRate: 16000, Channels: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	if _, err := rc.Read(buf); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { rc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close 应迅速杀掉 ffmpeg 并返回")
	}
}

func TestTranscodeAMRIfAvailable(t *testing.T) {
	tc := requireTC(t)
	if err := tc.CanEncode(FormatAMR, 8000); err != nil {
		t.Skipf("跳过：%v", err)
	}
	ctx := context.Background()
	src := writeTestWAV(t, 500)
	amrPath := filepath.Join(t.TempDir(), "out.amr")
	spec := Spec{Format: FormatAMR, SampleRate: 8000, Channels: 1}
	if err := tc.TranscodeFile(ctx, src, amrPath, spec); err != nil {
		t.Fatal(err)
	}
	info, err := tc.Probe(ctx, amrPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != FormatAMR || info.SampleRate != 8000 {
		t.Fatalf("amr 探测异常：%+v", info)
	}
}
