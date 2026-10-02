package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	FirstImage          byte = '2'
	ImageHeaderBytes         = 100
	ImageHeadMagic           = uint32(0x5050)
	ImageStageUploading      = uint32(1)
	ImageStageFinished       = uint32(2)
	ImageStageBreak          = uint32(3)
	MaxImageSliceBytes       = 51200
	PhotoBehavior            = 601
)

// ImageHeader 对齐基线 common/types/newProtocol.go 的图片帧头（小端 100 字节）。
type ImageHeader struct {
	Head           uint32
	Stage          uint32
	SequenceNumber uint32
	UUID           uint32
	Total          uint32
	TotalSize      uint32
	SliceTotal     uint32
	SliceIndex     uint32
	SliceSize      uint32
	QuestionKey    [16]byte
	ImageFormat    [8]byte
	Reserved       [40]byte
}

var ErrImageHeaderTooShort = errors.New("image header 不足 100 字节")

func EncodeImageHeader(h ImageHeader) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &h); err != nil {
		return nil, err
	}
	if buf.Len() != ImageHeaderBytes {
		return nil, fmt.Errorf("ImageHeader 编码长度为 %d，期望 %d", buf.Len(), ImageHeaderBytes)
	}
	return buf.Bytes(), nil
}

func DecodeImageHeader(b []byte) (ImageHeader, error) {
	var h ImageHeader
	if len(b) < ImageHeaderBytes {
		return h, ErrImageHeaderTooShort
	}
	if err := binary.Read(bytes.NewReader(b[:ImageHeaderBytes]), binary.LittleEndian, &h); err != nil {
		return h, err
	}
	return h, nil
}

func EncodeImageFrame(h ImageHeader, payload []byte) ([]byte, error) {
	hdr, err := EncodeImageHeader(h)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(hdr)+len(payload))
	out[0] = FirstImage
	copy(out[1:], hdr)
	copy(out[1+len(hdr):], payload)
	return out, nil
}

// BuildImageFrames questionKey 为空 = 带图送话（phase14）：服务端只存图，
// 等同 UUID 的语音轮来取；非空 = 指令拍照（phase12）。
func BuildImageFrames(img []byte, format, questionKey, replyFormat string, uuid uint32) ([][]byte, error) {
	switch {
	case len(img) == 0:
		return nil, errors.New("空图")
	case len(questionKey) > 16:
		return nil, errors.New("QuestionKey 超过 16 字节")
	case format == "":
		return nil, errors.New("格式为空")
	case len(format) > 8:
		return nil, errors.New("格式超过 8 字节")
	case len(replyFormat) > 40:
		return nil, errors.New("replyFormat 超过 40 字节")
	case uuid < 1 || uuid > 0x7FFFFFFF:
		return nil, errors.New("uuid 超出 1..0x7FFFFFFF")
	}
	n := (len(img) + MaxImageSliceBytes - 1) / MaxImageSliceBytes
	frames := make([][]byte, 0, n)
	for i := range n {
		start := i * MaxImageSliceBytes
		end := min(start+MaxImageSliceBytes, len(img))
		payload := img[start:end]
		stage := ImageStageUploading
		if i == n-1 {
			stage = ImageStageFinished
		}
		h := ImageHeader{
			Head:           ImageHeadMagic,
			Stage:          stage,
			SequenceNumber: uint32(i),
			UUID:           uuid,
			Total:          1,
			TotalSize:      uint32(len(img)),
			SliceTotal:     uint32(n),
			SliceIndex:     uint32(i),
			SliceSize:      uint32(len(payload)),
		}
		copy(h.QuestionKey[:], questionKey)
		copy(h.ImageFormat[:], format)
		copy(h.Reserved[:], replyFormat)
		frame, err := EncodeImageFrame(h, payload)
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// ImageAck 对齐基线 types.ImageAck：服务端对图像分片/合并的应答。
type ImageAck struct {
	SequenceNumber uint32   `json:"sequence_number"`
	UUID           uint32   `json:"uuid"`
	Code           uint32   `json:"code"`
	Message        string   `json:"message"`
	Data           []uint32 `json:"data"`
}

func DecodeImageAck(data json.RawMessage) (ImageAck, error) {
	var a ImageAck
	err := json.Unmarshal(data, &a)
	return a, err
}

type PhotoCommand struct {
	QuestionKey string
	StartText   string
	StartVoice  string
}

func DecodePhotoCommand(data json.RawMessage) (PhotoCommand, bool) {
	var wrap struct {
		Movement struct {
			Behavior   int    `json:"behavior"`
			StartText  string `json:"start_text"`
			StartVoice string `json:"start_voice"`
		} `json:"movement"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &wrap) != nil {
		return PhotoCommand{}, false
	}
	if wrap.Movement.Behavior != PhotoBehavior {
		return PhotoCommand{}, false
	}
	var inner struct {
		QuestionKey string `json:"QuestionKey"`
	}
	if json.Unmarshal(wrap.Data, &inner) != nil || inner.QuestionKey == "" {
		return PhotoCommand{}, false
	}
	return PhotoCommand{
		QuestionKey: inner.QuestionKey,
		StartText:   wrap.Movement.StartText,
		StartVoice:  wrap.Movement.StartVoice,
	}, true
}
