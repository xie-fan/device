package api

import (
	"time"

	"toy-device-simulator/config"
	"toy-device-simulator/core"
)

const (
	stCreated  = "created"
	stStarting = "starting"
	stRunning  = "running"
	stStopping = "stopping"
	stStopped  = "stopped"
)

type managedDevice struct {
	id           string
	instanceID   string
	envName      string // 配置树环境名；enterprise/device_type 简称在 cfg 里
	cfg          config.Device
	state        string
	gen          int
	committed    map[int]bool
	inst         *core.DeviceInstance
	log          *core.EventLog
	fault        core.Fault
	permitHeld   bool
	lastError    string
	lastActivity time.Time
	turns        map[string]*turnRec
	playingMode  int
}

type turnRec struct {
	TurnID       string
	InstanceID   string
	UplinkUUID   uint32
	SeqBefore    int
	EndReason    string
	UplinkReason string
	ReplyKind    string
	SampleRate   int
	Channels     int
	OutputDir    string
}

type tombstone struct {
	deviceID   string
	instanceID string
	expires    time.Time
	log        *core.EventLog
	turns      map[string]*turnRec
	cfg        config.Device
	gen        int
}

type assetObj struct {
	id           string
	path         string
	epoch        int
	bytes        int
	durationMs   int
	sampleRate   int
	channels     int
	sampleFormat string
}

type scenarioRun struct {
	ID     string       `json:"run_id"`
	Status string       `json:"status"`
	Steps  []stepResult `json:"steps"`
}

type stepResult struct {
	Index          int    `json:"index"`
	Status         string `json:"status"`
	InstanceID     string `json:"instance_id"`
	ConnGeneration int    `json:"conn_generation"`
	TurnID         string `json:"turn_id"`
	SeqBefore      int    `json:"seq_before"`
	Error          string `json:"error,omitempty"`
}

func (d *managedDevice) syncRunning() {
	if d == nil || d.inst == nil {
		return
	}
	if d.state != stStarting && d.state != stRunning {
		return
	}
	if core.Speakable(d.inst.ConnectionState(), d.fault) {
		d.state = stRunning
	}
}

func (d *managedDevice) connState() string {
	if d == nil || d.inst == nil {
		return core.ConnDisconnected.String()
	}
	return d.inst.ConnectionState().String()
}

func deviceView(d *managedDevice) map[string]any {
	d.syncRunning()
	backlog := 0
	if d.inst != nil {
		backlog = d.inst.BacklogLen()
	}
	return map[string]any{
		"device_id":         d.id,
		"instance_id":       d.instanceID,
		"instance_state":    d.state,
		"connection_state":  d.connState(),
		"conn_generation":   d.gen,
		"last_activity":     d.lastActivity.UTC().Format(time.RFC3339Nano),
		"playing_mode":      d.playingMode,
		"last_error":        d.lastError,
		"environment":       d.envName,
		"enterprise":        d.cfg.Enterprise,
		"device_type":       d.cfg.DeviceType,
		"speak_backlog_len": backlog,
	}
}
