package main

// 下行音频的格式与分包。真实服务端的下行格式跟随上行格式
// （common/client/reply.go 的 audioConfig.Format = c.Format），且每种格式的切包规则
// 不同——那些规则是迁就设备固件的，不按编码规范来，所以这里照抄而非重新设计：
//
//	amr → 每包独立「文件式」，包首带 #!AMR 存储头 + 整数个帧
//	aac → 20 KB 字节硬切，首尾可为半 ADTS 帧
//	mp3 → 帧对齐切分
//	pcm → 无帧结构，按字节等分
//
// 无 ffmpeg / 缺编码器 / 转码失败一律退回 pcm，本地既有验收不回退。

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"toy-device-simulator/core"
	"toy-device-simulator/media"
)

// maxPacketBytes 物理包字节上限，对齐服务端 AAC 的 20 KB 硬切。
const maxPacketBytes = 20 * 1024

// buildDownlink 生成 totalMs 的 440Hz 正弦音，转到 format 并按该格式的规则分包。
// 返回实际使用的格式——转不出时退回 pcm，调用方据此填帧头。
func buildDownlink(tc *media.Toolchain, format string, rate, totalMs int) ([][]byte, string) {
	pcm := sinePCM(rate, totalMs)
	if format == media.FormatPCM || format == media.FormatWAV || tc == nil {
		return splitBytes(pcm, maxPacketBytes), media.FormatPCM
	}
	enc, err := transcodeSine(tc, pcm, format, rate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "echosrv: 下行转 %s 失败，退回 pcm：%v\n", format, err)
		return splitBytes(pcm, maxPacketBytes), media.FormatPCM
	}
	switch format {
	case media.FormatAMR:
		return splitAMR(enc, rate), format
	case media.FormatMP3:
		return splitMP3(enc), format
	default: // aac：20 KB 硬切，首尾可为半 ADTS 帧
		return splitBytes(enc, maxPacketBytes), format
	}
}

// sinePCM 生成 s16le 单声道 440Hz 正弦。
func sinePCM(rate, totalMs int) []byte {
	n := rate * totalMs / 1000
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// transcodeSine 把裸 PCM 包成 WAV 落临时文件后交给 ffmpeg 转到目标格式。
func transcodeSine(tc *media.Toolchain, pcm []byte, format string, rate int) ([]byte, error) {
	dir, err := os.MkdirTemp("", "echosrv-tts-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src.wav")
	wav := core.EncodeWAV(core.PCM{Samples: pcm, SampleRate: rate, Channels: 1, BitsPerSample: 16})
	if err := os.WriteFile(src, wav, 0o644); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, "out."+format)
	spec := media.Spec{Format: format, SampleRate: rate, Channels: 1}
	if err := tc.TranscodeFile(context.Background(), src, dst, spec); err != nil {
		return nil, err
	}
	return os.ReadFile(dst)
}

// splitBytes 按 max 字节等分（pcm 无帧结构、aac 按服务端规则硬切）。
func splitBytes(b []byte, max int) [][]byte {
	if len(b) == 0 {
		return nil
	}
	var out [][]byte
	for off := 0; off < len(b); off += max {
		end := off + max
		if end > len(b) {
			end = len(b)
		}
		out = append(out, b[off:end])
	}
	return out
}

var (
	amrNBHeader = []byte("#!AMR\n")
	amrWBHeader = []byte("#!AMR-WB\n")
	// 帧字节数按 ToC 首字节的 mode（含 ToC 自身）；-1 = 保留模式，视为坏帧。
	amrNBSizes = [16]int{13, 14, 16, 18, 20, 21, 27, 32, 6, -1, -1, -1, -1, -1, -1, 1}
	amrWBSizes = [16]int{18, 24, 33, 37, 41, 47, 51, 59, 61, 6, -1, -1, -1, -1, 1, 1}
)

// splitAMR 剥掉整段的存储头后逐帧解析，每个物理包重新带上存储头——
// 服务端就是这么发的（固件按整包解析），所以落盘文件里会出现多个头。
func splitAMR(b []byte, rate int) [][]byte {
	header, sizes := amrWBHeader, amrWBSizes
	if rate == 8000 {
		header, sizes = amrNBHeader, amrNBSizes
	}
	body := b
	if len(body) >= len(header) && string(body[:len(header)]) == string(header) {
		body = body[len(header):]
	}
	var out [][]byte
	cur := append([]byte(nil), header...)
	for off := 0; off < len(body); {
		size := sizes[(body[off]>>3)&0x0F]
		if size <= 0 || off+size > len(body) {
			break // 坏帧或截断，丢弃残余
		}
		if len(cur)+size > maxPacketBytes && len(cur) > len(header) {
			out = append(out, cur)
			cur = append([]byte(nil), header...)
		}
		cur = append(cur, body[off:off+size]...)
		off += size
	}
	if len(cur) > len(header) {
		out = append(out, cur)
	}
	return out
}

// mp3 Layer III 帧长表（下标：bitrate index / sample-rate index）。
var (
	mp3BitrateV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3BitrateV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	mp3RateV1    = [4]int{44100, 48000, 32000, 0}
	mp3RateV2    = [4]int{22050, 24000, 16000, 0}
	mp3RateV25   = [4]int{11025, 12000, 8000, 0}
)

// mp3FrameLen 解析 4 字节帧头，返回整帧字节数；0 = 不是合法 Layer III 帧头。
func mp3FrameLen(h []byte) int {
	if len(h) < 4 || h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return 0
	}
	verBits := (h[1] >> 3) & 0x03 // 00=2.5 10=2 11=1
	if verBits == 1 || (h[1]>>1)&0x03 != 0x01 {
		return 0 // 保留版本，或非 Layer III
	}
	brIdx, srIdx, pad := (h[2]>>4)&0x0F, (h[2]>>2)&0x03, int((h[2]>>1)&0x01)
	var bitrate, rate, coef int
	if verBits == 3 { // MPEG1
		bitrate, rate, coef = mp3BitrateV1[brIdx], mp3RateV1[srIdx], 144
	} else {
		bitrate, coef = mp3BitrateV2[brIdx], 72
		if verBits == 2 {
			rate = mp3RateV2[srIdx]
		} else {
			rate = mp3RateV25[srIdx]
		}
	}
	if bitrate == 0 || rate == 0 {
		return 0
	}
	return coef*bitrate*1000/rate + pad
}

// splitMP3 帧对齐切分：包边界永远落在 MPEG 帧边界上，不切断帧。
func splitMP3(b []byte) [][]byte {
	var out [][]byte
	start, off := 0, 0
	for off < len(b) {
		n := mp3FrameLen(b[off:])
		if n <= 0 || off+n > len(b) {
			break // 遇到非帧数据（如残留标签）或截断，收尾
		}
		if off+n-start > maxPacketBytes && off > start {
			out = append(out, b[start:off])
			start = off
		}
		off += n
	}
	if off > start {
		out = append(out, b[start:off])
	}
	return out
}
