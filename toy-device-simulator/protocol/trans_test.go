package protocol

import (
	"encoding/json"
	"testing"
)

func TestCommandDataDecodesAllFields(t *testing.T) {
	raw := json.RawMessage(`{
		"sequence_number": 7, "code": 0, "message": "ok",
		"setVolume": 60, "setTimbre": "news", "shutDown": true,
		"playingMode": 2, "total": 2, "light": 1, "fan": 2,
		"movements": [{"behavior": 601, "angle": 90, "distance": 10,
			"start_text": "a", "end_text": "b", "start_voice": "v", "end_voice": "w"}],
		"movement": {"behavior": 301},
		"data": {"QuestionKey": "q1"},
		"need_ack": 1
	}`)
	c, err := DecodeCommandData(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.SequenceNumber != 7 || c.SetVolume != 60 || c.SetTimbre != "news" ||
		!c.ShutDown || c.PlayingMode != 2 || c.Total != 2 ||
		c.Light != 1 || c.Fan != 2 || c.NeedAck != 1 {
		t.Fatalf("标量字段不全: %+v", c)
	}
	if len(c.Movements) != 1 || c.Movements[0].Behavior != 601 ||
		c.Movements[0].Angle != 90 || c.Movements[0].StartText != "a" {
		t.Fatalf("movements 不全: %+v", c.Movements)
	}
	if c.Movement.Behavior != 301 {
		t.Fatalf("movement.behavior=%d", c.Movement.Behavior)
	}
	var inner struct {
		QuestionKey string `json:"QuestionKey"`
	}
	if err := json.Unmarshal(c.Data, &inner); err != nil || inner.QuestionKey != "q1" {
		t.Fatalf("data=%s err=%v", c.Data, err)
	}
}

func TestTransRequestFrameRoundTrip(t *testing.T) {
	var td TransferData
	td.DeviceID = "dev1"
	td.Enterprise = "ent"
	td.DeviceType = "A3"
	td.EventType = "open_api"
	td.Timestamp = 1730000000
	td.Request.Path = "/open/echo"
	td.Request.Header = map[string]interface{}{"x": "y"}
	td.Request.Body = map[string]interface{}{"k": "v"}
	raw, err := EncodeTransFrame(td)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[0] != FirstTrans {
		t.Fatal("首字节应为 '3'")
	}
	got, err := DecodeTransRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "dev1" || got.Enterprise != "ent" || got.DeviceType != "A3" ||
		got.EventType != "open_api" || got.Request.Path != "/open/echo" ||
		got.Request.Body["k"] != "v" {
		t.Fatalf("往返不一致: %+v", got)
	}
}

func TestTransDownlinkDecodesEnvelope(t *testing.T) {
	var td TransferData
	td.Request.Path = "/open/echo"
	td.Response.StatusCode = 200
	td.Response.Body = map[string]interface{}{"r": 1}
	data, _ := json.Marshal(td)
	env, _ := json.Marshal(Envelope{Topic: "ent/A3/dev1/trans/client", Data: data})
	msg := append([]byte{FirstTrans}, env...)

	gotEnv, got, err := DecodeTransDownlink(msg)
	if err != nil {
		t.Fatal(err)
	}
	if gotEnv.Topic != "ent/A3/dev1/trans/client" {
		t.Fatalf("topic=%s", gotEnv.Topic)
	}
	if got.Request.Path != "/open/echo" || got.Response.StatusCode != 200 {
		t.Fatalf("data=%+v", got)
	}
	if _, _, err := DecodeTransDownlink([]byte{FirstManage, '{'}); err == nil {
		t.Fatal("'1' 帧不应解成 trans")
	}
}

func TestDecodeImageAck(t *testing.T) {
	a, err := DecodeImageAck(json.RawMessage(
		`{"sequence_number": 3, "uuid": 9, "code": 1005, "message": "miss", "data": [1, 4]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.SequenceNumber != 3 || a.UUID != 9 || a.Code != 1005 || len(a.Data) != 2 || a.Data[1] != 4 {
		t.Fatalf("%+v", a)
	}
}
