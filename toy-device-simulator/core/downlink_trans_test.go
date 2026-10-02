package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

// waitEventType 等设备事件流里出现某个事件类型，返回该事件。
func waitEventType(t *testing.T, d *DeviceInstance, typ string, timeout time.Duration) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range d.Events() {
			if ev.Type == typ {
				return ev
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待事件 %s 超时", typ)
	return Event{}
}

func noProtocolError(t *testing.T, d *DeviceInstance) {
	t.Helper()
	for _, ev := range d.Events() {
		if ev.Type == "protocol_error" {
			t.Fatalf("不应产生 protocol_error: %+v", ev)
		}
	}
}

func TestImageAckDownlinkEvent(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(protocol.ImageAck{SequenceNumber: 3, UUID: 9, Code: 1005, Data: []uint32{1, 4}})
	env, _ := json.Marshal(protocol.Envelope{Topic: "demo/A3/sim_001/upload_image_slice/client", Data: data})
	conn.Push(append([]byte{protocol.FirstImage}, env...))

	ev := waitEventType(t, d, "image_ack", time.Second)
	if !strings.Contains(ev.Reason, "code=1005") || !strings.Contains(ev.Reason, "missing=2") {
		t.Fatalf("reason=%q", ev.Reason)
	}
	noProtocolError(t, d)
}

func TestImageBinaryDownlinkEvent(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	h := protocol.ImageHeader{Head: protocol.ImageHeadMagic, Stage: protocol.ImageStageUploading,
		SequenceNumber: 2, UUID: 9, SliceIndex: 2, SliceSize: 3}
	frame, err := protocol.EncodeImageFrame(h, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	conn.Push(frame)

	ev := waitEventType(t, d, "image_downlink", time.Second)
	if !strings.Contains(ev.Reason, "stage=1") {
		t.Fatalf("reason=%q", ev.Reason)
	}
	noProtocolError(t, d)
}

func TestTransDownlinkEvent(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	var td protocol.TransferData
	td.Request.Path = "/open/echo"
	td.Response.StatusCode = 200
	data, _ := json.Marshal(td)
	env, _ := json.Marshal(protocol.Envelope{Topic: "demo/A3/sim_001/trans/client", Data: data})
	conn.Push(append([]byte{protocol.FirstTrans}, env...))

	ev := waitEventType(t, d, "trans_response", time.Second)
	if !strings.Contains(ev.Reason, "path=/open/echo") || !strings.Contains(ev.Reason, "status=200") {
		t.Fatalf("reason=%q", ev.Reason)
	}
	noProtocolError(t, d)
}

func TestBadTransDownlinkIsProtocolError(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	conn.Push(append([]byte{protocol.FirstTrans}, []byte("not json")...))
	ev := waitEventType(t, d, "protocol_error", time.Second)
	if !strings.Contains(ev.Reason, "转发下行") {
		t.Fatalf("reason=%q", ev.Reason)
	}
}

func TestCommandReceivedReasonSummary(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, conn := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.EncodeManage("demo/A3/sim_001/command/client", json.RawMessage(`{
		"sequence_number": 7, "playingMode": 2, "setVolume": 60,
		"shutDown": true, "light": 1, "fan": 2,
		"movements": [{"behavior": 601}, {"behavior": 205}],
		"need_ack": 1
	}`))
	if err != nil {
		t.Fatal(err)
	}
	conn.Push(raw)

	ev := waitEventType(t, d, "command_received", time.Second)
	for _, want := range []string{"seq=7", "playingMode=2", "setVolume=60", "shutDown", "light=1", "fan=2", "movements=2[601,205]", "need_ack"} {
		if !strings.Contains(ev.Reason, want) {
			t.Fatalf("reason=%q 缺 %q", ev.Reason, want)
		}
	}
}

func TestEncodeStage3UsesConfiguredFormat(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Audio.Format = "wav"
	d, _ := newTestDevice(t, cfg, FaultNone, autoOpts{})
	raw := d.encodeStage3(42, "")
	view := protocol.Inspect(raw)
	if !view.OKHeader {
		t.Fatal("应能解出音频头")
	}
	if got := strings.TrimRight(string(view.Header.AudioFormat[:]), "\x00"); got != "wav" {
		t.Fatalf("stage3 帧头格式=%q 应为 wav", got)
	}
	if view.Header.Stage != protocol.StageBreak {
		t.Fatalf("stage=%d", view.Header.Stage)
	}
}

func TestVADStage2UsesUplinkFormat(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Audio.Format = "wav"
	cfg.Audio.SliceMs = 10
	// holdWrites 堵住所有写出，注册/上报走不通；skip_register 保持 Connected 即可 speak。
	d, conn := newTestDevice(t, cfg, FaultSkipRegister, autoOpts{holdWrites: true})
	if err := d.Start(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	pcm := uplinkPCM(cfg, 24)
	_, uuid, err := d.Speak(pcm)
	if err != nil {
		t.Fatal(err)
	}
	waitQueuedAudio(t, d, 6, time.Second)

	vad, err := protocol.EncodeAudioFrame(protocol.NewPCMHeader(protocol.StageVAD, 0, uuid, 0, uint32(cfg.Audio.SampleRate)), nil)
	if err != nil {
		t.Fatal(err)
	}
	waitGap(t, d, uuid, protocol.StageFinished, func() { conn.Push(vad) })
	releaseWriteGate(conn)
	waitUUIDStages(t, conn, uuid, 2*time.Second, func(s []uint32) bool {
		return hasStage(s, protocol.StageFinished)
	})
	found := false
	for _, w := range conn.Writes() {
		view := protocol.Inspect(w)
		if !view.OKHeader || view.Header.UUID != uuid || view.Header.Stage != protocol.StageFinished {
			continue
		}
		found = true
		if got := strings.TrimRight(string(view.Header.AudioFormat[:]), "\x00"); got != "wav" {
			t.Fatalf("VAD 触发的 stage2 帧头格式=%q 应为 wav", got)
		}
	}
	if !found {
		t.Fatal("未写出 stage2 帧")
	}
}

func TestWavUplinkHeaderFormat(t *testing.T) {
	frames := BuildUplinkFrames(make([]byte, 3200), 3, 16000, 100, FaultNone, 0, "wav")
	if len(frames) < 2 {
		t.Fatal("应有 Stage=1 与 Stage=2")
	}
	for i, f := range frames {
		h, err := protocol.DecodeHeader(f[1:])
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimRight(string(h.AudioFormat[:]), "\x00"); got != "wav" {
			t.Fatalf("帧 %d 格式=%q 应为 wav", i, got)
		}
	}
}
