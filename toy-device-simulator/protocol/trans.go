package protocol

import (
	"encoding/json"
	"errors"
)

const FirstTrans byte = '3'

// TransferData 对齐基线 websocket/controller/trans.go 的转发消息。
// 上行是 '3' + 本结构扁平 JSON；下行是 '3' + {topic:"…/trans/client", data:本结构}，
// 服务端回填 Response。
type TransferData struct {
	DeviceID   string `json:"device_id"`
	Enterprise string `json:"enterprise"`
	DeviceType string `json:"device_type"`
	EventType  string `json:"event_type"`
	Timestamp  int64  `json:"timestamp"`
	Request    struct {
		Path   string                 `json:"path"`
		Header map[string]interface{} `json:"header"`
		Body   map[string]interface{} `json:"body"`
	} `json:"request"`
	Response struct {
		StatusCode int                    `json:"status_code"`
		Header     map[string]interface{} `json:"header"`
		Body       map[string]interface{} `json:"body"`
	} `json:"response"`
}

func EncodeTransFrame(d TransferData) ([]byte, error) {
	body, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(body))
	out[0] = FirstTrans
	copy(out[1:], body)
	return out, nil
}

func DecodeTransRequest(msg []byte) (TransferData, error) {
	var d TransferData
	if len(msg) == 0 || msg[0] != FirstTrans {
		return d, errors.New("不是 '3' 转发帧")
	}
	err := json.Unmarshal(msg[1:], &d)
	return d, err
}

// DecodeTransDownlink 解下行 '3' 信封：返回信封与 data 里的 TransferData。
func DecodeTransDownlink(msg []byte) (Envelope, TransferData, error) {
	var env Envelope
	var d TransferData
	if len(msg) == 0 || msg[0] != FirstTrans {
		return env, d, errors.New("不是 '3' 转发帧")
	}
	if err := json.Unmarshal(msg[1:], &env); err != nil {
		return env, d, err
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return env, d, err
	}
	return env, d, nil
}
