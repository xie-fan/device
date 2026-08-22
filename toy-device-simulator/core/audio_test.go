package core

import (
	"bytes"
	"testing"
)

func TestDecodeWAVRejectsNonRIFF(t *testing.T) {
	if _, err := DecodeWAV([]byte("not a wav")); err != ErrNotWAV {
		t.Fatalf("err=%v", err)
	}
}

func TestWAVMustMatchDeviceAudioFingerprint(t *testing.T) {
	pcm := PCM{Samples: bytes.Repeat([]byte{0, 1}, 1600), SampleRate: 8000, Channels: 1, BitsPerSample: 16}
	wav := EncodeWAV(pcm)
	got, err := DecodeWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Match(16000, 1, "s16le"); err != ErrAudioShape {
		t.Fatalf("8kHz 应对 16k 设备失败, err=%v", err)
	}
}

func TestSlicePCMIsRawBytesNotNestedWAV(t *testing.T) {
	raw := bytes.Repeat([]byte{0x12, 0x34}, 3200) // 6400 B
	parts := SlicePCM(raw, 16000, 1, 16, 100)
	if len(parts) != 2 || len(parts[0]) != 3200 {
		t.Fatalf("片数=%d 首片=%d", len(parts), len(parts[0]))
	}
	for i, p := range parts {
		if bytes.Contains(p, []byte("RIFF")) {
			t.Fatalf("第 %d 片含 RIFF，禁止逐片封装 WAV", i)
		}
	}
}

func TestSpeakCopyCompletesBeforeOccupy(t *testing.T) {
	slot := NewSlot()
	wav := EncodeWAV(PCM{Samples: bytes.Repeat([]byte{1, 0}, 160), SampleRate: 16000, Channels: 1, BitsPerSample: 16})
	decoded, err := DecodeWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Match(16000, 1, "s16le"); err != nil {
		t.Fatal(err)
	}
	if slot.Occupied() {
		t.Fatal("读 WAV / 拷贝 PCM 完成前不得占槽")
	}
	if err := slot.Occupy("t1", 7, decoded.Samples); err != nil {
		t.Fatal(err)
	}
	decoded.Samples[0] = 0xff
	if slot.PCM()[0] == 0xff {
		t.Fatal("槽内必须是拷贝，不能与源切片共享底层数组")
	}
}

func TestDecodeFailureDoesNotOccupySlot(t *testing.T) {
	slot := NewSlot()
	if _, err := DecodeWAV([]byte("RIFF????")); err == nil {
		t.Fatal("坏 WAV 应失败")
	}
	if slot.Occupied() {
		t.Fatal("解码失败不占槽")
	}
}
