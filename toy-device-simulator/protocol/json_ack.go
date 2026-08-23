package protocol

// JSONAck 对齐协议文档的 downlink-ack JSON data。
type JSONAck struct {
	Ack            uint32 `json:"ack"`
	DownlinkType   string `json:"downlink_type"`
	Topic          string `json:"topic,omitempty"`
	UUID           uint32 `json:"uuid,omitempty"`
	SequenceNumber uint32 `json:"sequence_number"`
	Code           uint32 `json:"code"`
	Message        string `json:"message"`
	MemoryPercent  uint32 `json:"memory_percent"`
	SleepMs        uint32 `json:"sleep_ms"`
	MemoryTotalKB  uint32 `json:"memory_total_kb"`
	MemoryFreeKB   uint32 `json:"memory_free_kb"`
}

// EncodeJSONAckFrame 产出 '1' + topic {enterprise}/{device_type}/{device_id}/downlink-ack/server。
func EncodeJSONAckFrame(enterprise, deviceType, deviceID string, ack JSONAck) ([]byte, error) {
	topic := Topic(enterprise, deviceType, deviceID, "downlink-ack", "server")
	return EncodeManage(topic, ack)
}

// WritesThrottleCache 报告该 sleep_ms 是否写入节流缓存。0 不写、也不能清旧值。
func WritesThrottleCache(sleepMs int) bool {
	return sleepMs != 0
}

// SleepThrottle 运行时节流缓存：sleep_ms=0 不得覆盖已有 Last。
type SleepThrottle struct {
	Last int
	Set  bool
}

func (s *SleepThrottle) Observe(sleepMs int) {
	if WritesThrottleCache(sleepMs) {
		s.Last = sleepMs
		s.Set = true
	}
}
