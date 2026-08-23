package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type File struct {
	Device Device `yaml:"device"`
}

type Device struct {
	Enterprise      string    `yaml:"enterprise"`
	DeviceType      string    `yaml:"device_type"`
	DeviceID        string    `yaml:"device_id"`
	Action          string    `yaml:"action"`
	FirmwareVersion string    `yaml:"firmware_version"`
	NicType         string    `yaml:"nic_type"`
	NicICCID        string    `yaml:"nic_iccid"`
	PlayingMode     int       `yaml:"playing_mode"`
	Audio           Audio     `yaml:"audio"`
	Behavior        Behavior  `yaml:"behavior"`
	UUID            UUIDRange `yaml:"uuid"`
	Server          Server    `yaml:"server"`
	Recording       Recording `yaml:"recording"`
}

type Audio struct {
	Format         string `yaml:"format"`
	SampleRate     int    `yaml:"sample_rate"`
	Channels       int    `yaml:"channels"`
	SampleFormat   string `yaml:"sample_format"`
	SliceMs        int    `yaml:"slice_ms"`
	MaxPayloadSize int    `yaml:"max_payload_size"`
}

type Behavior struct {
	// 指针区分 YAML 省略与显式 false；省略在 Load 时缺省 true。
	AutoRegister           *bool       `yaml:"auto_register"`
	AutoReport             *bool       `yaml:"auto_report"`
	KeepaliveIntervalSec   int         `yaml:"keepalive_interval_sec"`
	KeepaliveMethod        string      `yaml:"keepalive_method"`
	ReportSequenceStart    int         `yaml:"report_sequence_start"`
	ReportEchoTimeoutSec   int         `yaml:"report_echo_timeout_sec"`
	RegisterAckTimeoutSec  int         `yaml:"register_ack_timeout_sec"`
	FirstReplyTimeoutSec   int         `yaml:"first_reply_timeout_sec"`
	DownlinkIdleTimeoutSec int         `yaml:"downlink_idle_timeout_sec"`
	NonAudioFollowupSec    int         `yaml:"non_audio_followup_sec"`
	PostFinalASRSilenceSec int         `yaml:"post_final_asr_silence_sec"`
	WaitTimeoutSlackSec    int         `yaml:"wait_timeout_slack_sec"`
	WriteQueueDepth        int         `yaml:"write_queue_depth"`
	WriteDrainTimeoutSec   int         `yaml:"write_drain_timeout_sec"`
	ExpectDownlinkNeedAck  bool        `yaml:"expect_downlink_need_ack"`
	DownlinkAck            DownlinkAck `yaml:"downlink_ack"`
}

type DownlinkAck struct {
	Mode    string `yaml:"mode"`
	SleepMs int    `yaml:"sleep_ms"`
	Code    int    `yaml:"code"`
}

type UUIDRange struct {
	Min int `yaml:"min"`
	Max int `yaml:"max"`
}

type Server struct {
	URL string `yaml:"url"`
}

type Recording struct {
	EnableFrameLog    bool   `yaml:"enable_frame_log"`
	SaveUplinkAudio   bool   `yaml:"save_uplink_audio"`
	SaveDownlinkAudio bool   `yaml:"save_downlink_audio"`
	OutputDir         string `yaml:"output_dir"`
}

func LoadFile(path string) (Device, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Device{}, err
	}
	return Load(raw)
}

func Load(raw []byte) (Device, error) {
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return Device{}, err
	}
	applyPhase1Defaults(&f.Device)
	if err := Validate(f.Device); err != nil {
		return Device{}, err
	}
	return f.Device, nil
}

// applyPhase1Defaults 把省略的 auto_register / auto_report 缺省为 true。
// 显式 false 保持 false，由 Validate 拒绝；禁止映射成 skip_register / skip_report。
func applyPhase1Defaults(d *Device) {
	if d.Behavior.AutoRegister == nil {
		v := true
		d.Behavior.AutoRegister = &v
	}
	if d.Behavior.AutoReport == nil {
		v := true
		d.Behavior.AutoReport = &v
	}
}

func Validate(d Device) error {
	if d.Enterprise == "" || d.DeviceType == "" || d.DeviceID == "" {
		return fmt.Errorf("enterprise/device_type/device_id 必填")
	}
	if err := ValidatePathComponent(d.DeviceID); err != nil {
		return fmt.Errorf("device_id: %w", err)
	}
	// MH 前缀会命中 core 的 Seq 不重置例外，用例会假通过；硬拒绝，不可绕过。
	if strings.HasPrefix(d.DeviceType, "MH") {
		return fmt.Errorf("device_type 不得以 MH 前缀开头（否则 Seq 用例会假通过）")
	}
	if d.Action != "chatbot" {
		return fmt.Errorf("action 必须为 chatbot")
	}
	if d.PlayingMode < 1 || d.PlayingMode > 3 {
		return fmt.Errorf("非法 playing_mode=%d", d.PlayingMode)
	}
	if d.Audio.Format != "pcm" {
		return fmt.Errorf("Phase 1 仅允许 format=pcm，得到 %q", d.Audio.Format)
	}
	if d.Audio.Channels != 1 {
		return fmt.Errorf("Phase 1 仅允许 channels=1，得到 %d", d.Audio.Channels)
	}
	if d.Audio.SampleFormat != "s16le" {
		return fmt.Errorf("Phase 1 仅允许 sample_format=s16le，得到 %q", d.Audio.SampleFormat)
	}
	if d.Audio.SliceMs <= 0 {
		return fmt.Errorf("slice_ms 必须 > 0")
	}
	if d.Audio.SampleRate <= 0 {
		return fmt.Errorf("sample_rate 必须 > 0")
	}
	if d.Behavior.AutoRegister != nil && !*d.Behavior.AutoRegister {
		return fmt.Errorf("Phase 1 拒绝 auto_register=false（仅允许 true + --inject）")
	}
	if d.Behavior.AutoReport != nil && !*d.Behavior.AutoReport {
		return fmt.Errorf("Phase 1 拒绝 auto_report=false（仅允许 true + --inject）")
	}
	if d.Behavior.DownlinkAck.Mode == "json" {
		return fmt.Errorf("Phase 1 拒绝 json ACK")
	}
	if d.Behavior.DownlinkAck.SleepMs != 0 {
		return fmt.Errorf("Phase 1 要求 sleep_ms=0")
	}
	if d.Behavior.WriteQueueDepth < 2 {
		return fmt.Errorf("write_queue_depth 必须 >= 2")
	}
	if d.Behavior.WriteDrainTimeoutSec <= 0 {
		return fmt.Errorf("write_drain_timeout_sec 必须 > 0")
	}
	if d.UUID.Min < 1 || d.UUID.Max > 2147483647 || d.UUID.Min > d.UUID.Max {
		return fmt.Errorf("uuid 范围非法")
	}
	if d.Server.URL == "" {
		return fmt.Errorf("server.url 必填")
	}
	return nil
}

// ValidatePathComponent 拒绝会逃出 output_dir 的 device_id / turn_id。
// output_dir 本身允许相对路径（如 ./recordings），不要用本函数校验它。
func ValidatePathComponent(id string) error {
	if id == "" {
		return fmt.Errorf("不得为空")
	}
	if strings.IndexByte(id, 0) >= 0 {
		return fmt.Errorf("不得含 NUL")
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("不得含路径分隔符")
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("不得含 ..")
	}
	if filepath.IsAbs(id) {
		return fmt.Errorf("不得为绝对路径")
	}
	if len(id) >= 2 && id[1] == ':' && isDriveLetter(id[0]) {
		return fmt.Errorf("不得含 Windows 盘符")
	}
	return nil
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// IsLoopbackServerURL 判断 server.url 主机是否为 localhost / 127.0.0.1 / ::1。
func IsLoopbackServerURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
