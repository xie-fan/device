package main

import (
	"os"
	"path/filepath"

	"toy-device-simulator/core"
	"toy-device-simulator/protocol"
)

func main() {
	root := filepath.Join("testdata")
	gf := filepath.Join(root, "golden_frames")
	_ = os.MkdirAll(gf, 0o755)
	_ = os.MkdirAll(filepath.Join(root, "audio"), 0o755)

	h1, _ := protocol.EncodeHeader(protocol.NewPCMHeader(protocol.StageUploading, 0, 1, 0, 16000))
	h2, _ := protocol.EncodeHeader(protocol.NewPCMHeader(protocol.StageFinished, 1, 1, 0, 16000))
	mustWrite(filepath.Join(gf, "stage1_seq0_pcm.hdr"), h1)
	mustWrite(filepath.Join(gf, "stage2_empty.hdr"), h2)

	ack, _ := protocol.EncodeAck(protocol.AudioBinaryAck(0, protocol.DownlinkTTS, 0))
	mustWrite(filepath.Join(gf, "ack_tts_seq0.bin"), ack)

	wav := core.EncodeWAV(core.PCM{
		Samples:       make([]byte, 3200),
		SampleRate:    16000,
		Channels:      1,
		BitsPerSample: 16,
	})
	mustWrite(filepath.Join(root, "audio", "hello.wav"), wav)
	mustWrite(filepath.Join(root, "hello.wav"), wav)
}

func mustWrite(path string, b []byte) {
	if err := os.WriteFile(path, b, 0o644); err != nil {
		panic(err)
	}
}
