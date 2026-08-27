package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"toy-device-simulator/core"
	"toy-device-simulator/manager"
)

// Options 测试与进程入口共用。
type Options struct {
	Config        manager.Config
	Dial          core.DialFunc
	TemplatesDir  string
	RecordingsDir string
	// RegistryPath 配置树落盘路径；空则 configs/registry.yaml。
	RegistryPath   string
	AfterAssetStat func()
	// TTL 测试覆盖 event log / 录音目录的过期时间；0 则用 Config.EventLogTTLHours。
	TTL time.Duration
}

type Server struct {
	opts Options
	mux  *http.ServeMux
	reg  *manager.Registry
	bus  *globalBus

	mu        sync.Mutex
	devices   map[string]*managedDevice
	tombs     map[string]*tombstone // instance_id
	connUsed  int
	speakUsed atomic.Int64
	runs      map[string]*scenarioRun

	assetMu sync.Mutex
	assets  map[string]*assetObj
}

func New(opts Options) (http.Handler, error) {
	regPath := opts.RegistryPath
	if regPath == "" {
		regPath = filepath.Join("configs", "registry.yaml")
	}
	reg, err := manager.LoadRegistry(regPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:    opts,
		reg:     reg,
		bus:     newGlobalBus(opts.Config.EventLogMaxEntries),
		devices: map[string]*managedDevice{},
		tombs:   map[string]*tombstone{},
		assets:  map[string]*assetObj{},
		runs:    map[string]*scenarioRun{},
	}
	mux := http.NewServeMux()
	s.mux = mux
	mux.HandleFunc("POST /assets", s.handlePostAsset)
	mux.HandleFunc("GET /assets/{id}", s.handleGetAsset)
	mux.HandleFunc("GET /assets/{id}/content", s.handleGetAssetContent)
	mux.HandleFunc("DELETE /assets/{id}", s.handleDeleteAsset)

	mux.HandleFunc("POST /devices", s.handlePostDevices)
	mux.HandleFunc("GET /devices", s.handleListDevices)
	mux.HandleFunc("GET /devices/{id}", s.handleGetDevice)
	mux.HandleFunc("DELETE /devices/{id}", s.handleDeleteDevice)
	mux.HandleFunc("GET /devices/{id}/config", s.handleGetConfig)
	mux.HandleFunc("PUT /devices/{id}/config", s.handlePutConfig)

	mux.HandleFunc("POST /devices/{id}/start", s.handleStart)
	mux.HandleFunc("POST /devices/{id}/wait_ready", s.handleWaitReady)
	mux.HandleFunc("POST /devices/{id}/stop", s.handleStop)
	mux.HandleFunc("POST /devices/{id}/speak", s.handleSpeak)
	mux.HandleFunc("POST /devices/{id}/speak_and_wait", s.handleSpeakAndWait)
	mux.HandleFunc("POST /devices/{id}/interrupt", s.handleInterrupt)
	mux.HandleFunc("POST /devices/{id}/report", s.handleReport)
	mux.HandleFunc("POST /devices/{id}/faults", s.handleFaults)

	mux.HandleFunc("GET /devices/{id}/turns", s.handleListTurns)
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}", s.handleGetTurn)
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}/frames", s.handleGetFrames)
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}/audio/uplink", s.handleGetAudioUplink)
	mux.HandleFunc("GET /devices/{id}/turns/{turn_id}/audio/downlink", s.handleGetAudioDownlink)
	mux.HandleFunc("GET /devices/{id}/events", s.handleGetEvents)

	mux.HandleFunc("POST /devices/batch/start", s.handleBatchStart)
	mux.HandleFunc("POST /devices/batch/stop", s.handleBatchStop)
	mux.HandleFunc("POST /devices/batch/delete", s.handleBatchDelete)

	mux.HandleFunc("POST /templates", s.handlePostTemplate)
	mux.HandleFunc("GET /templates", s.handleListTemplates)
	mux.HandleFunc("GET /templates/{id}", s.handleGetTemplate)
	mux.HandleFunc("DELETE /templates/{id}", s.handleDeleteTemplate)

	mux.HandleFunc("POST /wait", s.handleWait)
	mux.HandleFunc("GET /ws/events", s.handleWSEvents)
	mux.HandleFunc("GET /ws/events/global", s.handleWSGlobalEvents)

	mux.HandleFunc("POST /scenarios/run", s.handleScenarioRun)
	mux.HandleFunc("GET /scenarios/runs/{id}", s.handleScenarioGet)

	mux.HandleFunc("GET /registry", s.handleGetRegistry)
	mux.HandleFunc("POST /registry/environments", s.handlePostEnvironment)
	mux.HandleFunc("PUT /registry/environments/{env}", s.handlePutEnvironment)
	mux.HandleFunc("DELETE /registry/environments/{env}", s.handleDeleteEnvironment)
	mux.HandleFunc("POST /registry/environments/{env}/enterprises", s.handlePostEnterprise)
	mux.HandleFunc("PUT /registry/environments/{env}/enterprises/{short}", s.handlePutEnterprise)
	mux.HandleFunc("DELETE /registry/environments/{env}/enterprises/{short}", s.handleDeleteEnterprise)
	mux.HandleFunc("POST /registry/environments/{env}/enterprises/{short}/device_types", s.handlePostDeviceType)
	mux.HandleFunc("PUT /registry/environments/{env}/enterprises/{short}/device_types/{tshort}", s.handlePutDeviceType)
	mux.HandleFunc("DELETE /registry/environments/{env}/enterprises/{short}/device_types/{tshort}", s.handleDeleteDeviceType)

	s.mountUI()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Close 停掉全部实例并排空 recorder，避免测试 TempDir 在 Windows 上删不干净。
func (s *Server) Close() error {
	s.mu.Lock()
	insts := make([]*core.DeviceInstance, 0, len(s.devices))
	for _, d := range s.devices {
		if d.inst != nil {
			insts = append(insts, d.inst)
		}
	}
	s.mu.Unlock()
	for _, inst := range insts {
		inst.Shutdown()
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func drainTimeout(cfg manager.Config) time.Duration {
	sec := cfg.WriteDrainTimeoutSec
	if sec <= 0 {
		sec = 2
	}
	return time.Duration(sec) * time.Second
}

func waitReadyDefault(cfg manager.Config) time.Duration {
	sec := cfg.WaitReadyTimeoutSec
	if sec <= 0 {
		sec = 30
	}
	return time.Duration(sec) * time.Second
}

func ttl(cfg manager.Config) time.Duration {
	h := cfg.EventLogTTLHours
	if h <= 0 {
		h = 24
	}
	return time.Duration(h) * time.Hour
}

func (s *Server) eventTTL() time.Duration {
	if s.opts.TTL > 0 {
		return s.opts.TTL
	}
	return ttl(s.opts.Config)
}
