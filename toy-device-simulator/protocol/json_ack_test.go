package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONAckFrameStartsWithDigitOneAndTopicDownlinkAck(t *testing.T) {
	raw, err := EncodeJSONAckFrame("demo", "A3", "sim_001", JSONAck{
		Ack: 1, SequenceNumber: 1, DownlinkType: "tts", UUID: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[0] != FirstManage {
		t.Fatalf("JSON ACK 首字节应为 '1'，得到 %q", raw)
	}
	env, err := DecodeManage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(env.Topic, "/downlink-ack/server") {
		t.Fatalf("topic=%s，应以 /downlink-ack/server 结尾", env.Topic)
	}
	parts, err := SplitTopic(env.Topic)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0] != "demo" || parts[1] != "A3" || parts[2] != "sim_001" {
		t.Fatalf("topic 前三段=%v", parts)
	}
}

func TestJSONAckSleepMsZeroDoesNotWriteThrottleCache(t *testing.T) {
	if WritesThrottleCache(0) {
		t.Fatal("sleep_ms:0 不得写入节流缓存")
	}
	c := &SleepThrottle{}
	c.Observe(500)
	if !c.Set || c.Last != 500 {
		t.Fatal("非 0 的 sleep_ms 应写入节流缓存")
	}
	c.Observe(0)
	if c.Last == 0 {
		t.Fatal("sleep_ms:0 不得清掉已有节流值")
	}
}

func TestJSONAckAudioUsesAudioSeqAndDownlinkTypeTTSOrHint(t *testing.T) {
	for _, typ := range []string{"tts", "hint_audio"} {
		raw, err := EncodeJSONAckFrame("demo", "A3", "sim_001", JSONAck{
			Ack: 7, SequenceNumber: 7, DownlinkType: typ, UUID: 42, Code: 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		env, err := DecodeManage(raw)
		if err != nil {
			t.Fatal(err)
		}
		var data JSONAck
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Ack != 7 || data.SequenceNumber != 7 {
			t.Fatalf("音频 JSON ACK 的 ack/sequence_number 应为音频 Seq=7，得到 ack=%d seq=%d", data.Ack, data.SequenceNumber)
		}
		if data.DownlinkType != typ {
			t.Fatalf("downlink_type=%s，期望 %s", data.DownlinkType, typ)
		}
		if data.UUID != 42 {
			t.Fatalf("uuid 应为音频 UUID，得到 %d", data.UUID)
		}
	}
}

func TestJSONAckCommandUsesCommandSeqAndDownlinkTypeCommand(t *testing.T) {
	cmdTopic := "demo/A3/sim_001/command/client"
	raw, err := EncodeJSONAckFrame("demo", "A3", "sim_001", JSONAck{
		Ack: 5, SequenceNumber: 5, DownlinkType: "command", Topic: cmdTopic,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeManage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(env.Topic, "/downlink-ack/server") {
		t.Fatalf("信封 topic=%s", env.Topic)
	}
	var data JSONAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Ack != 5 || data.SequenceNumber != 5 {
		t.Fatalf("指令 JSON ACK 的 ack/sequence_number 应为指令序号 5，得到 ack=%d seq=%d", data.Ack, data.SequenceNumber)
	}
	if data.DownlinkType != "command" {
		t.Fatalf("downlink_type=%s，应为 command", data.DownlinkType)
	}
	if data.Topic != cmdTopic {
		t.Fatalf("data.topic 应为原指令完整 topic，得到 %s", data.Topic)
	}
	if data.UUID != 0 {
		t.Fatalf("指令 JSON ACK 的 uuid 应省略或 0，得到 %d", data.UUID)
	}
}
