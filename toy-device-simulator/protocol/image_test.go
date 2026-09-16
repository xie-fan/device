package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// Phase 12 拍照：'2' + 100 字节小端 ImageHeader + 分片。字节布局照抄真实服务端
// common/types/newProtocol.go 的 ImageHeader。

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off : off+4]) }

func TestImageFrameLayout(t *testing.T) {
	h := ImageHeader{
		Head: ImageHeadMagic, Stage: ImageStageFinished, SequenceNumber: 3, UUID: 77,
		Total: 1, TotalSize: 9, SliceTotal: 4, SliceIndex: 3, SliceSize: 2,
	}
	copy(h.QuestionKey[:], "0123456789abcdef")
	copy(h.ImageFormat[:], "jpg")
	copy(h.Reserved[:], "amr")
	frame, err := EncodeImageFrame(h, []byte{0xAA, 0xBB})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 1+ImageHeaderBytes+2 || frame[0] != FirstImage || FirstImage != '2' || ImageHeaderBytes != 100 {
		t.Fatalf("帧应为 '2' + 100 字节头 + 载荷，得到 len=%d first=%q", len(frame), frame[0])
	}
	b := frame[1:]
	checks := []struct {
		name string
		off  int
		want uint32
	}{
		{"Head", 0, 0x5050}, {"Stage", 4, 2}, {"SequenceNumber", 8, 3}, {"UUID", 12, 77},
		{"Total", 16, 1}, {"TotalSize", 20, 9}, {"SliceTotal", 24, 4}, {"SliceIndex", 28, 3}, {"SliceSize", 32, 2},
	}
	for _, c := range checks {
		if got := le32(b, c.off); got != c.want {
			t.Errorf("%s@%d 应为 %d，得到 %d", c.name, c.off, c.want, got)
		}
	}
	if string(b[36:52]) != "0123456789abcdef" {
		t.Errorf("QuestionKey@36..51 不符: %q", b[36:52])
	}
	if !bytes.Equal(b[52:60], append([]byte("jpg"), make([]byte, 5)...)) {
		t.Errorf("ImageFormat@52..59 应为 jpg 补 0: %v", b[52:60])
	}
	if !bytes.Equal(b[60:100], append([]byte("amr"), make([]byte, 37)...)) {
		t.Errorf("Reserved@60..99 应为 amr 补 0: %v", b[60:100])
	}
	if !bytes.Equal(b[100:], []byte{0xAA, 0xBB}) {
		t.Errorf("载荷应紧跟在头后面: %v", b[100:])
	}
	got, err := DecodeImageHeader(b)
	if err != nil || got != h {
		t.Fatalf("DecodeImageHeader 应还原帧头: %+v %v", got, err)
	}
	if _, err := DecodeImageHeader(b[:99]); err == nil {
		t.Fatal("不足 100 字节应报错")
	}
}

// 分片规则：每片 ≤ 51200；非末片 Stage=1、末片 Stage=2 且带数据；SequenceNumber = SliceIndex。
func TestBuildImageFramesSplits(t *testing.T) {
	img := make([]byte, 120000)
	for i := range img {
		img[i] = byte(i)
	}
	frames, err := BuildImageFrames(img, "jpg", "0123456789abcdef", "amr", 12345)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("120000 字节应切 3 片，得到 %d", len(frames))
	}
	var joined []byte
	for i, f := range frames {
		if f[0] != FirstImage {
			t.Fatalf("第 %d 片首字节应为 '2'", i)
		}
		h, err := DecodeImageHeader(f[1:])
		if err != nil {
			t.Fatal(err)
		}
		payload := f[1+ImageHeaderBytes:]
		wantStage := ImageStageUploading
		if i == len(frames)-1 {
			wantStage = ImageStageFinished
		}
		if h.Head != ImageHeadMagic || h.Stage != wantStage || h.SliceIndex != uint32(i) || h.SequenceNumber != uint32(i) ||
			h.SliceTotal != 3 || h.Total != 1 || h.TotalSize != 120000 || h.UUID != 12345 || h.SliceSize != uint32(len(payload)) {
			t.Fatalf("第 %d 片帧头不符: %+v", i, h)
		}
		if len(payload) == 0 || len(payload) > MaxImageSliceBytes || MaxImageSliceBytes != 51200 {
			t.Fatalf("第 %d 片载荷应在 1..51200，得到 %d", i, len(payload))
		}
		if string(bytes.TrimRight(h.QuestionKey[:], "\x00")) != "0123456789abcdef" ||
			string(bytes.TrimRight(h.ImageFormat[:], "\x00")) != "jpg" ||
			string(bytes.TrimRight(h.Reserved[:], "\x00")) != "amr" {
			t.Fatalf("第 %d 片的 QuestionKey/ImageFormat/Reserved 不符: %+v", i, h)
		}
		joined = append(joined, payload...)
	}
	if !bytes.Equal(joined, img) {
		t.Fatal("各片载荷拼起来应等于原图")
	}
}

func TestBuildImageFramesSingleSliceAndEmptyReserved(t *testing.T) {
	frames, err := BuildImageFrames([]byte{1, 2, 3}, "png", "k1", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("小图应只有 1 片，得到 %d", len(frames))
	}
	h, _ := DecodeImageHeader(frames[0][1:])
	if h.Stage != ImageStageFinished || h.SliceTotal != 1 || len(frames[0]) != 1+ImageHeaderBytes+3 {
		t.Fatalf("唯一的一片就是末片，Stage=2 且带数据: %+v", h)
	}
	if h.Reserved != ([40]byte{}) {
		t.Fatalf("replyFormat 为空时 Reserved 应全 0: %v", h.Reserved)
	}
}

func TestBuildImageFramesRejects(t *testing.T) {
	cases := []struct {
		name               string
		img                []byte
		format, key, reply string
		uuid               uint32
	}{
		{"空图", nil, "jpg", "k", "amr", 1},
		{"QuestionKey 超 16 字节", []byte{1}, "jpg", "0123456789abcdefX", "amr", 1},
		{"QuestionKey 为空", []byte{1}, "jpg", "", "amr", 1},
		{"格式超 8 字节", []byte{1}, "jpegjpegx", "k", "amr", 1},
		{"格式为空", []byte{1}, "", "k", "amr", 1},
		{"UUID 为 0", []byte{1}, "jpg", "k", "amr", 0},
		{"UUID 超 0x7FFFFFFF", []byte{1}, "jpg", "k", "amr", 0x80000000},
	}
	for _, c := range cases {
		if _, err := BuildImageFrames(c.img, c.format, c.key, c.reply, c.uuid); err == nil {
			t.Errorf("%s 应报错", c.name)
		}
	}
}

func TestDecodePhotoCommand(t *testing.T) {
	// 真实服务端 service/skills/camera.go 下发的 data。
	raw := json.RawMessage(`{"code":0,"message":"","sequence_number":0,"total":1,` +
		`"movement":{"behavior":601,"start_text":"好的，我拍一下","start_voice":"AAEC"},` +
		`"data":{"Num":1,"QuestionKey":"0123456789abcdef"},"need_ack":1}`)
	cmd, ok := DecodePhotoCommand(raw)
	if !ok || cmd.QuestionKey != "0123456789abcdef" || cmd.StartText != "好的，我拍一下" || cmd.StartVoice != "AAEC" {
		t.Fatalf("应识别为拍照指令: %+v %v", cmd, ok)
	}
	notPhoto := []string{
		`{"movement":{"behavior":600},"data":{"QuestionKey":"k"}}`,
		`{"movement":{"behavior":601},"data":{"Num":1}}`,
		`{"movement":{"behavior":601},"data":"x"}`,
		`{"setVolume":50}`,
		`not json`,
	}
	for _, s := range notPhoto {
		if _, ok := DecodePhotoCommand(json.RawMessage(s)); ok {
			t.Errorf("%s 不应识别为拍照指令", s)
		}
	}
}
