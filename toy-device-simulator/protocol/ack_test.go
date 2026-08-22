package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestAckBinarySizeIs28(t *testing.T) {
	if n := binary.Size(DeviceAckMsg{}); n != AckBytes {
		t.Fatalf("binary.Size=%d", n)
	}
}

func TestPhase1AckSleepMsIsZero(t *testing.T) {
	m := AudioBinaryAck(3, DownlinkTTS, 0)
	if m.SleepMs != 0 {
		t.Fatalf("SleepMs=%d", m.SleepMs)
	}
	raw, err := EncodeAckFrame(m)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != FirstAck {
		t.Fatalf("首字节=%q", raw[0])
	}
	got, err := DecodeAck(raw[1:])
	if err != nil {
		t.Fatal(err)
	}
	if got.Ack != 3 || got.DownlinkType != DownlinkTTS || got.SleepMs != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestCommandAckDownlinkTypeIs3(t *testing.T) {
	m := CommandBinaryAck(0, 0)
	if m.DownlinkType != DownlinkCommand {
		t.Fatalf("DownlinkType=%d", m.DownlinkType)
	}
}

func TestHintAudioAckDownlinkTypeIs2(t *testing.T) {
	m := AudioBinaryAck(1, DownlinkHintAudio, 0)
	raw, err := EncodeAck(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw[4:8], []byte{2, 0, 0, 0}) {
		t.Fatalf("DownlinkType 字节=%x", raw[4:8])
	}
}
