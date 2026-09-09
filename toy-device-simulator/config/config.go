package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"toy-device-simulator/media"
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
	Format       string `yaml:"format"`
	SampleRate   int    `yaml:"sample_rate"`
	Channels     int    `yaml:"channels"`
	SampleFormat string `yaml:"sample_format"`
	SliceMs      int    `yaml:"slice_ms"`
	// BitrateKbps 压缩格式（mp3/amr/aac）的编码码率；0=格式默认
	// （mp3=128 aac=96 amr-nb=12.2 amr-wb=23.85），amr 会就近合法档位。
	// pcm/wav 必须为 0。
	BitrateKbps    float64 `yaml:"bitrate_kbps"`
	MaxPayloadSize int     `yaml:"max_payload_size"`
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
	// Phase 4 speak backlog：Turn 排队深度，0=关闭（槽占用仍 409）。
	// 与 writePump 的 outbound buffer（write_queue_depth）不是同一功能。
	SpeakBacklogDepth int `yaml:"speak_backlog_depth"`
	// Phase 4 静默成功探针：turn 以 timeout 终态且全程无下行包时，发一次
	// 探针 report 借 echo 区分「静默成功」与「被 drop」。默认关。
	SilenceProbe bool `yaml:"silence_probe"`
	// Phase 4 断开即 interrupt：事件 WS 订阅 abort（断开/半开）时打断当前
	// turn（CancelTurn，不拆连接）。默认关。
	InterruptOnDisconnect bool `yaml:"interrupt_on_disconnect"`
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
	return loadWith(raw, Validate)
}

// LoadPhase2 供 Manager/API 创建设备：允许 json ACK 与非零 sleep_ms。
func LoadPhase2(raw []byte) (Device, error) {
	return loadWith(raw, ValidatePhase2)
}

func loadWith(raw []byte, validate func(Device) error) (Device, error) {
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return Device{}, err
	}
	applyPhase1Defaults(&f.Device)
	if err := validate(f.Device); err != nil {
		return Device{}, err
	}
	return f.Device, nil
}

// applyPhase1Defaults 把省略的 auto_register / auto_report 缺省为 true。
// 显式 false 保持 false，由 Validate 拒绝；禁止映射成 skip_register / skip_report。
// 跳过握手只走 CLI --inject 或 POST /devices/{id}/faults。
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
	if err := validateCommon(d); err != nil {
		return err
	}
	if d.Behavior.DownlinkAck.Mode == "json" {
		return fmt.Errorf("Phase 1 拒绝 json ACK")
	}
	if d.Behavior.DownlinkAck.SleepMs != 0 {
		return fmt.Errorf("Phase 1 要求 sleep_ms=0")
	}
	if d.Behavior.SpeakBacklogDepth != 0 {
		return fmt.Errorf("speak_backlog_depth 是 Phase 4 Manager 设备功能，Phase 1 CLI 拒绝")
	}
	if d.Behavior.SilenceProbe {
		return fmt.Errorf("silence_probe 是 Phase 4 Manager 设备功能，Phase 1 CLI 拒绝")
	}
	if d.Behavior.InterruptOnDisconnect {
		return fmt.Errorf("interrupt_on_disconnect 是 Phase 4 Manager 设备功能，Phase 1 CLI 拒绝")
	}
	if d.Audio.Format != "pcm" {
		return fmt.Errorf("Phase 1 仅允许 format=pcm，得到 %q", d.Audio.Format)
	}
	return nil
}

// ValidatePhase2 允许 json ACK 与非零 SleepMs；其余与 Phase 1 相同。
func ValidatePhase2(d Device) error {
	if err := validateCommon(d); err != nil {
		return err
	}
	mode := d.Behavior.DownlinkAck.Mode
	if mode != "" && mode != "binary" && mode != "json" {
		return fmt.Errorf("downlink_ack.mode 非法: %s", mode)
	}
	if d.Behavior.DownlinkAck.SleepMs < 0 {
		return fmt.Errorf("sleep_ms 不得为负")
	}
	if d.Behavior.SpeakBacklogDepth < 0 || d.Behavior.SpeakBacklogDepth > 64 {
		return fmt.Errorf("speak_backlog_depth 必须在 0..64（0=关闭）")
	}
	return nil
}

// IsSeqExemptDeviceType 报告该机型是否命中服务端的 Seq 不重置例外
// （ai-creates-wealth 的 isNonResetSequenceNumberDeviceType，判据就是 MH 前缀）。
// 这类机型上错误序号不会被丢，bad_seq 用例会假通过——所以拦在跑用例的地方，
// 而不是拦在建配置的地方：模拟真实 MH 机型本身是正当需求。
func IsSeqExemptDeviceType(deviceType string) bool {
	return strings.HasPrefix(deviceType, "MH")
}

func validateCommon(d Device) error {
	if d.Enterprise == "" || d.DeviceType == "" || d.DeviceID == "" {
		return fmt.Errorf("enterprise/device_type/device_id 必填")
	}
	if err := ValidatePathComponent(d.DeviceID); err != nil {
		return fmt.Errorf("device_id: %w", err)
	}
	if d.Action != "chatbot" {
		return fmt.Errorf("action 必须为 chatbot")
	}
	if d.PlayingMode < 1 || d.PlayingMode > 3 {
		return fmt.Errorf("非法 playing_mode=%d", d.PlayingMode)
	}
	// Phase 5：格式白名单 pcm/wav/mp3/amr/aac（压缩格式经 ffmpeg 转码/推流）。
	if !media.IsTargetFormat(d.Audio.Format) {
		return fmt.Errorf("format 仅支持 %s，得到 %q", strings.Join(media.TargetFormats, "/"), d.Audio.Format)
	}
	if d.Audio.Format == media.FormatAMR && d.Audio.SampleRate != 8000 && d.Audio.SampleRate != 16000 {
		return fmt.Errorf("amr 采样率仅支持 8000（NB）/16000（WB），得到 %d", d.Audio.SampleRate)
	}
	if d.Audio.BitrateKbps < 0 {
		return fmt.Errorf("bitrate_kbps 不得为负")
	}
	if d.Audio.BitrateKbps != 0 && !media.Compressed(d.Audio.Format) {
		return fmt.Errorf("bitrate_kbps 仅压缩格式（mp3/amr/aac）可设，format=%s", d.Audio.Format)
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
		return fmt.Errorf("auto_register 只能为 true，禁止 false（不得映射为 skip_register）")
	}
	if d.Behavior.AutoReport != nil && !*d.Behavior.AutoReport {
		return fmt.Errorf("auto_report 只能为 true，禁止 false（不得映射为 skip_report）")
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
