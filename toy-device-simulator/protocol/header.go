package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// HeaderBytes 与基线 types.AudioHeader / MqttHeaderLength 一致。
	HeaderBytes = 100
	HeadMagic   = uint32(0x4848)

	StageUploading = uint32(1)
	StageFinished  = uint32(2)
	StageBreak     = uint32(3)
	StageVAD       = uint32(4)
	StageWakeBreak = uint32(5)

	FirstAudio  byte = '0'
	FirstManage byte = '1'
	FirstAck    byte = '4'
	FirstJSON   byte = '{'
)

// AudioHeader 对齐基线 common/types/newProtocol.go。
// Pad 在基线里是空白字段 _ [2]byte，线上仍占 2 字节。
type AudioHeader struct {
	Head            uint32
	Stage           uint32
	SequenceNumber  uint32
	UUID            uint32
	AudioFormat     [10]byte
	Pad             [2]byte
	SamplingRate    uint32
	AudioPayloadLen uint32
	NeedAck         uint32
	Reserved        [60]byte
}

var (
	ErrHeaderTooShort = errors.New("audio header 不足 100 字节")
	ErrEmptyFrame     = errors.New("空帧")
)

func FormatBytes(s string) [10]byte {
	var out [10]byte
	copy(out[:], []byte(s))
	return out
}

// NewAudioHeader 构造指定线上格式的音频帧头（format 写入 10 字节格式字段，
// 如 pcm/wav/mp3/amr/aac）。
func NewAudioHeader(format string, stage, seq, uuid, payloadLen, sampleRate uint32) AudioHeader {
	return AudioHeader{
		Head:            HeadMagic,
		Stage:           stage,
		SequenceNumber:  seq,
		UUID:            uuid,
		AudioFormat:     FormatBytes(format),
		SamplingRate:    sampleRate,
		AudioPayloadLen: payloadLen,
	}
}

// NewPCMHeader 兼容包装：format=pcm。
func NewPCMHeader(stage, seq, uuid, payloadLen, sampleRate uint32) AudioHeader {
	return NewAudioHeader("pcm", stage, seq, uuid, payloadLen, sampleRate)
}

func EncodeHeader(h AudioHeader) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &h); err != nil {
		return nil, err
	}
	if buf.Len() != HeaderBytes {
		return nil, fmt.Errorf("AudioHeader 编码长度为 %d，期望 %d", buf.Len(), HeaderBytes)
	}
	return buf.Bytes(), nil
}

func DecodeHeader(b []byte) (AudioHeader, error) {
	var h AudioHeader
	if len(b) < HeaderBytes {
		return h, ErrHeaderTooShort
	}
	if err := binary.Read(bytes.NewReader(b[:HeaderBytes]), binary.LittleEndian, &h); err != nil {
		return h, err
	}
	return h, nil
}

func EncodeAudioFrame(h AudioHeader, payload []byte) ([]byte, error) {
	h.AudioPayloadLen = uint32(len(payload))
	hdr, err := EncodeHeader(h)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(hdr)+len(payload))
	out[0] = FirstAudio
	copy(out[1:], hdr)
	copy(out[1+len(hdr):], payload)
	return out, nil
}

func Classify(msg []byte) (first byte, rest []byte, err error) {
	if len(msg) == 0 {
		return 0, nil, ErrEmptyFrame
	}
	return msg[0], msg[1:], nil
}
