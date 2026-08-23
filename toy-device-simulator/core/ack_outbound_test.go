package core

import (
	"strings"
	"testing"

	"toy-device-simulator/protocol"
)

func TestAckSleepMsZeroDoesNotOverrideThrottle(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Behavior.DownlinkAck.SleepMs = 0
	d := NewDevice(cfg, Options{})
	t.Cleanup(func() { d.Shutdown() })
	d.throttle.Observe(500)
	d.deviceMu.Lock()
	d.enqueueAckLocked(1, protocol.DownlinkTTS, 9, "")
	d.deviceMu.Unlock()
	last, set := d.ThrottleLast()
	if !set || last != 500 {
		t.Fatalf("sleep_ms=0 不得覆盖已有节流 last=%d set=%v", last, set)
	}
}

func TestJSONAckOutboundFirstByteAndTopic(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Behavior.DownlinkAck.Mode = "json"
	cfg.Behavior.DownlinkAck.SleepMs = 500
	cfg.Behavior.DownlinkAck.Code = 1
	d := NewDevice(cfg, Options{})
	t.Cleanup(func() { d.Shutdown() })
	d.deviceMu.Lock()
	d.enqueueAckLocked(7, protocol.DownlinkTTS, 42, "")
	d.writePumpMu.Lock()
	q := d.outbound.Queued()
	d.writePumpMu.Unlock()
	d.deviceMu.Unlock()
	if len(q) == 0 {
		t.Fatal("应入队 JSON ACK")
	}
	raw := q[0].Raw
	if len(raw) == 0 || raw[0] != protocol.FirstManage {
		t.Fatalf("mode=json 出站首字节应为 '1'，得到 %q", raw)
	}
	env, err := protocol.DecodeManage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Topic, "downlink-ack/server") {
		t.Fatalf("topic=%s 应含 downlink-ack/server", env.Topic)
	}
}

func TestBinaryAckOutboundFirstByteFour(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Behavior.DownlinkAck.Mode = "binary"
	d := NewDevice(cfg, Options{})
	t.Cleanup(func() { d.Shutdown() })
	d.deviceMu.Lock()
	d.enqueueAckLocked(3, protocol.DownlinkTTS, 1, "")
	d.writePumpMu.Lock()
	q := d.outbound.Queued()
	d.writePumpMu.Unlock()
	d.deviceMu.Unlock()
	if len(q) == 0 || len(q[0].Raw) == 0 || q[0].Raw[0] != protocol.FirstAck {
		t.Fatalf("mode=binary 出站首字节应为 '4'")
	}
}
