package core

import (
	"fmt"

	"toy-device-simulator/protocol"
)

type Fault string

const (
	FaultNone         Fault = ""
	FaultSkipRegister Fault = "skip_register"
	FaultSkipReport   Fault = "skip_report"
	FaultBadSeq       Fault = "bad_seq"
	FaultOversize     Fault = "oversize"
	FaultBadHeader    Fault = "bad_header"
	FaultDupUUID      Fault = "dup_uuid"
	FaultBadStage     Fault = "bad_stage"
)

func FaultExpectsDrop(f Fault) bool {
	switch f {
	case FaultSkipRegister, FaultBadSeq, FaultOversize, FaultBadHeader:
		return true
	default:
		return false
	}
}

// BuildUplinkFrames 按 fault 生成实际上行帧（必须能进 frames.jsonl outbound）。
// 切片按 Phase 1 的 1ch/16bit（Validate 已保证）。oversize 长度为 maxPayloadSize+1；≤0 回退 51200。
func BuildUplinkFrames(pcm []byte, uuid uint32, sampleRate uint32, sliceMs int, f Fault, maxPayloadSize int) [][]byte {
	parts := SlicePCM(pcm, int(sampleRate), 1, 16, sliceMs)
	if len(parts) == 0 {
		parts = [][]byte{{}}
	}
	var frames [][]byte
	startSeq := uint32(0)
	if f == FaultBadSeq {
		startSeq = 1
	}
	oversizeN := maxPayloadSize
	if oversizeN <= 0 {
		oversizeN = 51200
	}
	for i, p := range parts {
		payload := p
		if f == FaultOversize {
			payload = make([]byte, oversizeN+1)
		}
		stage := protocol.StageUploading
		seq := startSeq + uint32(i)
		frame, _ := protocol.EncodeAudioFrame(protocol.NewPCMHeader(stage, seq, uuid, 0, sampleRate), payload)
		if f == FaultBadHeader && i == 0 {
			frame = append([]byte{protocol.FirstAudio}, make([]byte, 40)...) // '0' + 不足 100
		}
		if f == FaultBadStage && i == 0 {
			frame, _ = protocol.EncodeAudioFrame(protocol.NewPCMHeader(99, seq, uuid, 0, sampleRate), payload)
		}
		frames = append(frames, frame)
	}
	if f == FaultDupUUID && len(frames) > 0 {
		frames = append([][]byte{append([]byte(nil), frames[0]...)}, frames...)
	}
	if f != FaultOversize && f != FaultBadHeader {
		fin, _ := protocol.EncodeAudioFrame(protocol.NewPCMHeader(protocol.StageFinished, startSeq+uint32(len(parts)), uuid, 0, sampleRate), nil)
		frames = append(frames, fin)
	}
	return frames
}

func ParseFault(s string) (Fault, error) {
	switch Fault(s) {
	case FaultNone, FaultSkipRegister, FaultSkipReport, FaultBadSeq, FaultOversize, FaultBadHeader, FaultDupUUID, FaultBadStage:
		return Fault(s), nil
	default:
		return "", fmt.Errorf("未知 --inject=%q", s)
	}
}
