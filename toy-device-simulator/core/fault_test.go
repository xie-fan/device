package core

import (
	"testing"
	"toy-device-simulator/protocol"
)

func TestFaultDropMatrix(t *testing.T) {
	if !FaultExpectsDrop(FaultSkipRegister) || FaultExpectsDrop(FaultSkipReport) {
		t.Fatal("skip_register 期望 drop；skip_report 不默认 drop")
	}
	if FaultExpectsDrop(FaultDupUUID) || FaultExpectsDrop(FaultBadStage) {
		t.Fatal("dup_uuid / bad_stage 不作 drop 验收")
	}
}

func TestBadSeqStartsAtLeastOne(t *testing.T) {
	frames := BuildUplinkFrames(make([]byte, 3200), 3, 16000, 100, FaultBadSeq)
	h, err := protocol.DecodeHeader(frames[0][1:])
	if err != nil {
		t.Fatal(err)
	}
	if h.SequenceNumber < 1 || h.Stage != protocol.StageUploading {
		t.Fatalf("bad_seq 应在 CAS 后发出 Seq>=1 的 Stage=1, 得到 seq=%d stage=%d", h.SequenceNumber, h.Stage)
	}
}

func TestOversizePayloadExceeds51200(t *testing.T) {
	frames := BuildUplinkFrames(make([]byte, 100), 3, 16000, 100, FaultOversize)
	if len(frames[0])-1-protocol.HeaderBytes <= 51200 {
		t.Fatalf("payload=%d", len(frames[0])-1-protocol.HeaderBytes)
	}
}

func TestBadHeaderIsShorterThan100AfterFirstByte(t *testing.T) {
	frames := BuildUplinkFrames(make([]byte, 100), 3, 16000, 100, FaultBadHeader)
	if len(frames[0])-1 >= protocol.HeaderBytes {
		t.Fatalf("header_len=%d, 应 < 100", len(frames[0])-1)
	}
	if frames[0][0] != protocol.FirstAudio {
		t.Fatal()
	}
}

func TestSkipRegisterStillSendsLegalAudioAndStage2(t *testing.T) {
	frames := BuildUplinkFrames(make([]byte, 3200), 3, 16000, 100, FaultSkipRegister)
	if len(frames) < 2 {
		t.Fatal("应有 Stage=1 与 Stage=2")
	}
	last, _ := protocol.DecodeHeader(frames[len(frames)-1][1:])
	if last.Stage != protocol.StageFinished {
		t.Fatalf("最后一帧 Stage=%d", last.Stage)
	}
}
