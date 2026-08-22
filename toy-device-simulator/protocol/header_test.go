package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "testdata", "golden_frames")
}

func TestAudioHeaderBinarySizeIs100(t *testing.T) {
	var h AudioHeader
	if n := binary.Size(h); n != HeaderBytes {
		t.Fatalf("binary.Size=%d, 期望 %d（缺 padding 会变成 98）", n, HeaderBytes)
	}
}

func TestEncodeHeaderMatchesHandwrittenGolden(t *testing.T) {
	// 独立于 Go struct 的手写小端布局：若有人删掉 Pad，SamplingRate 会前移。
	wantHex := "" +
		"48480000" + // Head 0x4848
		"01000000" + // Stage=1
		"00000000" + // Seq=0
		"01000000" + // UUID=1
		"70636d00000000000000" + // "pcm"
		"0000" + // Pad
		"803e0000" + // 16000
		"00000000" + // payload len
		"00000000" + // NeedAck
		"000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != HeaderBytes {
		t.Fatalf("手写 golden 长度 %d", len(want))
	}
	got, err := EncodeHeader(NewPCMHeader(StageUploading, 0, 1, 0, 16000))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("编码与手写 golden 不一致\ngot  %x\nwant %x", got, want)
	}
}

func TestGoldenFilesMatchEncode(t *testing.T) {
	dir := testdataDir(t)
	cases := []struct {
		file string
		h    AudioHeader
	}{
		{"stage1_seq0_pcm.hdr", NewPCMHeader(StageUploading, 0, 1, 0, 16000)},
		{"stage2_empty.hdr", NewPCMHeader(StageFinished, 1, 1, 0, 16000)},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got, err := EncodeHeader(tc.h)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, tc.file)
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读 golden：%v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s 与 Encode 不一致", tc.file)
			}
		})
	}
}

func TestDecodeRejectsShortHeader(t *testing.T) {
	_, err := DecodeHeader(bytes.Repeat([]byte{0}, 99))
	if err != ErrHeaderTooShort {
		t.Fatalf("err=%v, 期望 ErrHeaderTooShort", err)
	}
}

func TestAudioFrameStartsWithZeroAndPayloadIsAfter100(t *testing.T) {
	payload := bytes.Repeat([]byte{0x11}, 3200)
	frame, err := EncodeAudioFrame(NewPCMHeader(StageUploading, 0, 7, 0, 16000), payload)
	if err != nil {
		t.Fatal(err)
	}
	if frame[0] != FirstAudio {
		t.Fatalf("首字节=%q", frame[0])
	}
	if len(frame) != 1+HeaderBytes+len(payload) {
		t.Fatalf("帧长=%d", len(frame))
	}
	h, err := DecodeHeader(frame[1:])
	if err != nil {
		t.Fatal(err)
	}
	if h.AudioPayloadLen != uint32(len(payload)) {
		t.Fatalf("AudioPayloadLen=%d", h.AudioPayloadLen)
	}
	gotPayload := frame[1+HeaderBytes:]
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("payload 应对齐「帧长-1-100」，而不是只信头字段")
	}
}

func TestClassifyDoesNotUTF8Decode(t *testing.T) {
	// 下行 TTS 常含非 UTF-8 字节；Classify 必须按原始字节切。
	raw := append([]byte{FirstAudio}, bytes.Repeat([]byte{0xff}, 120)...)
	first, rest, err := Classify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if first != FirstAudio || !bytes.Equal(rest, raw[1:]) {
		t.Fatal("应按字节分流，禁止当字符串")
	}
}

func TestMockGoManageFirstByteIsNotASCIIOne(t *testing.T) {
	if byte(0x01) == FirstManage {
		t.Fatal("mock.go 的 0x01 不得被当成合法管理首字节")
	}
	if FirstManage != '1' {
		t.Fatalf("管理首字节应为 ASCII '1'，得到 %q", FirstManage)
	}
}
