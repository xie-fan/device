package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Envelope struct {
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

func EncodeManage(topic string, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(Envelope{Topic: topic, Data: raw})
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(body))
	out[0] = FirstManage
	copy(out[1:], body)
	return out, nil
}

func DecodeManage(msg []byte) (Envelope, error) {
	if len(msg) == 0 || msg[0] != FirstManage {
		return Envelope{}, errors.New("不是 '1' 管理信封")
	}
	var env Envelope
	if err := json.Unmarshal(msg[1:], &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

func Topic(enterprise, deviceType, deviceID, scene, target string) string {
	return strings.Join([]string{enterprise, deviceType, deviceID, scene, target}, "/")
}

func SplitTopic(topic string) (parts []string, err error) {
	parts = strings.Split(topic, "/")
	if len(parts) != 5 {
		return nil, fmt.Errorf("topic 须恰好 5 段，得到 %d", len(parts))
	}
	return parts, nil
}

type UnprefixedJSON struct {
	RequestID string          `json:"RequestID"`
	Code      int             `json:"Code"`
	CodeMsg   string          `json:"CodeMsg"`
	Data      json.RawMessage `json:"Data"`
	Action    string          `json:"Action"`
	SessionID string          `json:"SessionID"`
	Text      string          `json:"Text"`
	IsFinal   bool            `json:"IsFinal"`
}

func DecodeUnprefixedJSON(msg []byte) (UnprefixedJSON, error) {
	if len(msg) == 0 || msg[0] != FirstJSON {
		return UnprefixedJSON{}, errors.New("不是无前缀 JSON")
	}
	var u UnprefixedJSON
	if err := json.Unmarshal(msg, &u); err != nil {
		return UnprefixedJSON{}, err
	}
	return u, nil
}

func IsFailedJSON(code int) bool {
	return code == 1 || code == 14007
}
