package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const AckBytes = 28

const (
	DownlinkUnknown   uint32 = 0
	DownlinkTTS       uint32 = 1
	DownlinkHintAudio uint32 = 2
	DownlinkCommand   uint32 = 3
)

// DeviceAckMsg 对齐基线 types.DeviceAckMsg，28 字节小端。
type DeviceAckMsg struct {
	Ack           uint32
	DownlinkType  uint32
	Code          uint32
	MemoryPercent uint32
	SleepMs       uint32
	MemoryTotalKB uint32
	MemoryFreeKB  uint32
}

func EncodeAck(m DeviceAckMsg) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &m); err != nil {
		return nil, err
	}
	if buf.Len() != AckBytes {
		return nil, fmt.Errorf("DeviceAckMsg 编码长度为 %d，期望 %d", buf.Len(), AckBytes)
	}
	return buf.Bytes(), nil
}

func DecodeAck(b []byte) (DeviceAckMsg, error) {
	var m DeviceAckMsg
	if len(b) < AckBytes {
		return m, errors.New("binary ACK 不足 28 字节")
	}
	if err := binary.Read(bytes.NewReader(b[:AckBytes]), binary.LittleEndian, &m); err != nil {
		return m, err
	}
	return m, nil
}

func EncodeAckFrame(m DeviceAckMsg) ([]byte, error) {
	body, err := EncodeAck(m)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(body))
	out[0] = FirstAck
	copy(out[1:], body)
	return out, nil
}

func AudioBinaryAck(seq, downlinkType, code uint32) DeviceAckMsg {
	return DeviceAckMsg{Ack: seq, DownlinkType: downlinkType, Code: code, SleepMs: 0}
}

func CommandBinaryAck(seq, code uint32) DeviceAckMsg {
	return DeviceAckMsg{Ack: seq, DownlinkType: DownlinkCommand, Code: code, SleepMs: 0}
}
