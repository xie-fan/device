package protocol

import "encoding/json"

type RegisterRequest struct {
	SequenceNumber  int    `json:"sequence_number"`
	Enterprise      string `json:"enterprise"`
	DeviceType      string `json:"device_type"`
	DeviceID        string `json:"device_id"`
	FirmwareVersion string `json:"firmware_version"`
	NicType         string `json:"nic_type"`
	NicICCID        string `json:"nic_iccid"`
}

type RegisterAck struct {
	SequenceNumber int    `json:"sequence_number"`
	Code           int    `json:"code"`
	Message        string `json:"message"`
	NTPTime        string `json:"ntp_time"`
	Expiration     int    `json:"expiration"`
}

type ReportData struct {
	SequenceNumber int `json:"sequence_number"`
	Code           int `json:"code"`
	SignalStrength int `json:"signalStrength"`
	BatPowerLevel  int `json:"batPowerLevel"`
	PlayingMode    int `json:"playingMode"`
}

type CommandData struct {
	SequenceNumber int `json:"sequence_number"`
	NeedAck        int `json:"need_ack"`
}

func DecodeRegisterAck(data json.RawMessage) (RegisterAck, error) {
	var a RegisterAck
	err := json.Unmarshal(data, &a)
	return a, err
}

func DecodeReportData(data json.RawMessage) (ReportData, error) {
	var r ReportData
	err := json.Unmarshal(data, &r)
	return r, err
}

func DecodeCommandData(data json.RawMessage) (CommandData, error) {
	var c CommandData
	err := json.Unmarshal(data, &c)
	return c, err
}

type FrameView struct {
	First     byte
	Topic     string
	Header    AudioHeader
	HeaderLen int
	Payload   []byte
	OKHeader  bool
}

func Inspect(raw []byte) FrameView {
	if len(raw) == 0 {
		return FrameView{}
	}
	v := FrameView{First: raw[0]}
	rest := raw[1:]
	switch raw[0] {
	case FirstAudio:
		v.HeaderLen = len(rest)
		if len(rest) >= HeaderBytes {
			v.HeaderLen = HeaderBytes
			h, err := DecodeHeader(rest)
			if err == nil {
				v.OKHeader = true
				v.Header = h
				v.Payload = rest[HeaderBytes:]
			}
		}
	case FirstManage:
		env, err := DecodeManage(raw)
		if err == nil {
			v.Topic = env.Topic
			v.Payload = rest
		}
	case FirstJSON:
		v.Payload = raw
	}
	return v
}
