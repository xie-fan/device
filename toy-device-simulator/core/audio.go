package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	ErrNotWAV     = errors.New("不是 RIFF/WAVE")
	ErrNotPCM     = errors.New("WAV 不是 PCM")
	ErrAudioShape = errors.New("WAV 的采样参数与设备 audio_* 不符")
)

type PCM struct {
	Samples       []byte
	SampleRate    int
	Channels      int
	BitsPerSample int
}

func DecodeWAV(b []byte) (PCM, error) {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return PCM{}, ErrNotWAV
	}
	var pcm PCM
	i := 12
	for i+8 <= len(b) {
		id := string(b[i : i+4])
		sz := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		body := i + 8
		next := body + sz
		if next > len(b) {
			return PCM{}, fmt.Errorf("chunk %s 越界", id)
		}
		switch id {
		case "fmt ":
			if sz < 16 {
				return PCM{}, ErrNotPCM
			}
			if binary.LittleEndian.Uint16(b[body:body+2]) != 1 {
				return PCM{}, ErrNotPCM
			}
			pcm.Channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
			pcm.SampleRate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
			pcm.BitsPerSample = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
		case "data":
			pcm.Samples = append([]byte(nil), b[body:next]...)
		}
		if sz%2 == 1 {
			next++
		}
		i = next
	}
	if pcm.Samples == nil {
		return PCM{}, errors.New("WAV 无 data chunk")
	}
	return pcm, nil
}

func (p PCM) Match(sampleRate, channels int, sampleFormat string) error {
	wantBits := 16
	if sampleFormat != "s16le" {
		return ErrAudioShape
	}
	if p.SampleRate != sampleRate || p.Channels != channels || p.BitsPerSample != wantBits {
		return ErrAudioShape
	}
	return nil
}

func SlicePCM(pcm []byte, sampleRate, channels, bits, sliceMs int) [][]byte {
	bytesPerSample := bits / 8
	n := sampleRate * channels * bytesPerSample * sliceMs / 1000
	if n <= 0 {
		return nil
	}
	var out [][]byte
	for i := 0; i < len(pcm); i += n {
		end := i + n
		if end > len(pcm) {
			end = len(pcm)
		}
		out = append(out, append([]byte(nil), pcm[i:end]...))
	}
	return out
}

func EncodeWAV(p PCM) []byte {
	data := p.Samples
	block := p.Channels * p.BitsPerSample / 8
	byteRate := p.SampleRate * block
	buf := new(bytes.Buffer)
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+len(data)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(p.Channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(p.SampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(buf, binary.LittleEndian, uint16(block))
	_ = binary.Write(buf, binary.LittleEndian, uint16(p.BitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	return buf.Bytes()
}
