package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// hardenCancel 配置进程取消策略：Windows 上用 taskkill 杀整棵进程树
// （PATH 上常见 chocolatey shim 之类的包装器，Process.Kill 只能杀外壳、
// 真 ffmpeg 孙进程会残留占住文件），并设 WaitDelay 防止 Wait 永久阻塞。
func hardenCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		}
		return cmd.Process.Kill()
	}
}

// probeJSON ffprobe -of json 的裁剪映射。
type probeJSON struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
		BitRate    string `json:"bit_rate"`
		Duration   string `json:"duration"`
	} `json:"streams"`
}

// Probe 探测音频文件（容器、编码、采样率、声道、码率、时长）。
func (t *Toolchain) Probe(ctx context.Context, path string) (Info, error) {
	cmd := exec.CommandContext(ctx, t.FFprobe,
		"-v", "error", "-of", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return Info{}, fmt.Errorf("ffprobe 无法识别：%s", tail(ee.Stderr))
		}
		return Info{}, fmt.Errorf("ffprobe 执行失败：%w", err)
	}
	var pj probeJSON
	if err := json.Unmarshal(out, &pj); err != nil {
		return Info{}, fmt.Errorf("ffprobe 输出解析失败：%w", err)
	}
	info := Info{}
	for _, st := range pj.Streams {
		if st.CodecType != "audio" {
			continue
		}
		info.Codec = st.CodecName
		info.SampleRate, _ = strconv.Atoi(st.SampleRate)
		info.Channels = st.Channels
		if bps, _ := strconv.ParseFloat(st.BitRate, 64); bps > 0 {
			info.BitrateKbps = bps / 1000
		}
		if sec, _ := strconv.ParseFloat(st.Duration, 64); sec > 0 {
			info.DurationMs = int(sec * 1000)
		}
		break
	}
	if info.Codec == "" {
		return Info{}, fmt.Errorf("文件中没有音频流")
	}
	if info.BitrateKbps == 0 {
		if bps, _ := strconv.ParseFloat(pj.Format.BitRate, 64); bps > 0 {
			info.BitrateKbps = bps / 1000
		}
	}
	if info.DurationMs == 0 {
		if sec, _ := strconv.ParseFloat(pj.Format.Duration, 64); sec > 0 {
			info.DurationMs = int(sec * 1000)
		}
	}
	info.Format = normalizeFormat(pj.Format.FormatName, info.Codec)
	return info, nil
}

// TranscodeFile 将 src 转码为目标规格写入 dst（一次性，非实时）。
// dst 由调用方决定路径与扩展名；ffmpeg 直接写文件以便回填容器头。
func (t *Toolchain) TranscodeFile(ctx context.Context, src, dst string, spec Spec) error {
	if err := t.CanEncode(spec.Format, spec.SampleRate); err != nil {
		return err
	}
	codecArgs, mux, err := encodeArgs(spec)
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-v", "error", "-y", "-i", src, "-vn"}
	if spec.SampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(spec.SampleRate))
	}
	if spec.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(spec.Channels))
	}
	args = append(args, codecArgs...)
	args = append(args, muxerArgs(spec.Format)...)
	args = append(args, "-f", mux, dst)
	cmd := exec.CommandContext(ctx, t.FFmpeg, args...)
	hardenCancel(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg 转码失败：%v：%s", err, tail(stderr.Bytes()))
	}
	return nil
}

// StreamRealtime 以 -re（按输入实际播放速率）读取 src 并输出目标格式裸流。
// copyCodec=true 表示源已是目标格式，仅限速直通（-c:a copy）；
// 否则边转码边限速。返回的 ReadCloser 关闭时杀掉 ffmpeg 进程。
func (t *Toolchain) StreamRealtime(ctx context.Context, src string, spec Spec, copyCodec bool) (io.ReadCloser, error) {
	codecArgs, mux, err := encodeArgs(spec)
	if err != nil {
		return nil, err
	}
	// -re 按输入实际速率节流；initial_burst 置 0 关掉默认 0.5s 的起始突发
	// （模拟设备实时录音无预缓冲，需要 ffmpeg >= 6.1）。
	args := []string{"-hide_banner", "-v", "error", "-re", "-readrate_initial_burst", "0.0", "-i", src, "-vn"}
	if copyCodec {
		codecArgs = []string{"-c:a", "copy"}
	} else {
		if err := t.CanEncode(spec.Format, spec.SampleRate); err != nil {
			return nil, err
		}
		if spec.SampleRate > 0 {
			args = append(args, "-ar", strconv.Itoa(spec.SampleRate))
		}
		if spec.Channels > 0 {
			args = append(args, "-ac", strconv.Itoa(spec.Channels))
		}
	}
	args = append(args, codecArgs...)
	args = append(args, muxerArgs(spec.Format)...)
	args = append(args, "-f", mux, "pipe:1")

	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, t.FFmpeg, args...)
	hardenCancel(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("ffmpeg 启动失败：%w", err)
	}
	return &procReader{rc: stdout, cmd: cmd, ctx: cctx, cancel: cancel, stderr: &stderr}, nil
}

// procReader 包装 ffmpeg stdout：EOF 时回收进程并将非零退出转为读错误；
// Close 杀进程（Read 与 Close 可能并发，wait 只执行一次）。
type procReader struct {
	rc     io.ReadCloser
	cmd    *exec.Cmd
	ctx    context.Context
	cancel context.CancelFunc
	stderr *bytes.Buffer

	mu      sync.Mutex
	waited  bool
	waitErr error
}

func (p *procReader) Read(b []byte) (int, error) {
	n, err := p.rc.Read(b)
	if errors.Is(err, io.EOF) {
		if werr := p.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (p *procReader) Close() error {
	p.cancel()
	_ = p.wait()
	return nil
}

func (p *procReader) wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.waited {
		p.waited = true
		err := p.cmd.Wait()
		// 主动 cancel 杀掉属预期；其余非零退出是真实编码失败，带 stderr 报出。
		if err != nil && p.ctx.Err() == nil {
			p.waitErr = fmt.Errorf("ffmpeg 推流失败：%v：%s", err, tail(p.stderr.Bytes()))
		}
	}
	return p.waitErr
}

// tail 截取 stderr 末尾（错误信息通常在最后几行）。
func tail(b []byte) string {
	const max = 400
	s := bytes.TrimSpace(b)
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return string(s)
}
