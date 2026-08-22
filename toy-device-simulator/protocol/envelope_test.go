package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestManageEnvelopeHasASCIIOnePrefix(t *testing.T) {
	raw, err := EncodeManage("demo/A3/sim_001/register/server", map[string]any{
		"device_id": "sim_001",
		"code":      0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != FirstManage {
		t.Fatalf("首字节=%q", raw[0])
	}
	env, err := DecodeManage(raw)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := SplitTopic(env.Topic)
	if err != nil {
		t.Fatal(err)
	}
	if parts[3] != "register" || parts[4] != "server" {
		t.Fatalf("topic=%s", env.Topic)
	}
}

func TestUnprefixedJSONIsNotManageEnvelope(t *testing.T) {
	msg := []byte(`{"RequestID":"r1","Code":1,"CodeMsg":"音频处理失败，请稍后重试","Data":null}`)
	if msg[0] != FirstJSON {
		t.Fatal("无前缀 JSON 必须以 '{' 开头")
	}
	if _, err := DecodeManage(msg); err == nil {
		t.Fatal("不得把无前缀 JSON 当管理信封")
	}
	u, err := DecodeUnprefixedJSON(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !IsFailedJSON(u.Code) {
		t.Fatalf("Code=%d 应视为失败 JSON", u.Code)
	}
}

func TestFailedJSONIncludesQuotaCode(t *testing.T) {
	if !IsFailedJSON(14007) {
		t.Fatal("14007 必须走失败 JSON")
	}
	if IsFailedJSON(0) {
		t.Fatal("Code=0 不是失败")
	}
}

func TestASRResultUsesActionAndSessionID(t *testing.T) {
	msg, _ := json.Marshal(map[string]any{
		"Action":    "asr_result",
		"SessionID": "123",
		"Text":      "你好",
		"IsFinal":   true,
	})
	u, err := DecodeUnprefixedJSON(msg)
	if err != nil {
		t.Fatal(err)
	}
	if u.Action != "asr_result" || u.SessionID != "123" || !u.IsFinal {
		t.Fatalf("%+v", u)
	}
}

func TestTopicMustHaveFiveSegments(t *testing.T) {
	if _, err := SplitTopic("a/b/c/d"); err == nil {
		t.Fatal("4 段应拒绝")
	}
	got := Topic("demo", "A3", "sim_001", "report", "server")
	if got != "demo/A3/sim_001/report/server" {
		t.Fatalf("%s", got)
	}
}

func TestRegisterAckCodeZeroIsSuccess(t *testing.T) {
	raw, err := EncodeManage("demo/A3/sim_001/register/client", map[string]any{
		"code":    0,
		"message": "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeManage(raw)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Code != 0 {
		t.Fatalf("code=%d", data.Code)
	}
	if bytes.Contains(raw, []byte{0x01}) && raw[0] == 0x01 {
		t.Fatal("禁止 mock.go 的 0x01 前缀")
	}
}
