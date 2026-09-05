package api

import (
	"encoding/json"
	"net/http"
	"time"

	"toy-device-simulator/core"
)

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "device 不存在")
		return
	}
	if d.state != stCreated && d.state != stStopped {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "仅 Created/Stopped 可 start")
		return
	}
	if !s.acquireConnLocked() {
		s.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "conn_permit")
		return
	}
	s.refreshServerURLLocked(d)
	d.gen++
	d.state = stStarting
	d.lastError = ""
	d.permitHeld = true
	d.committed[d.gen] = false
	d.lastActivity = time.Now()
	inst := s.spawnInstance(d)
	d.inst = inst
	gen := d.gen
	ins := d.instanceID
	s.mu.Unlock()

	go s.runStart(d, inst, gen)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"device_id":       id,
		"instance_id":     ins,
		"conn_generation": gen,
	})
}

// interruptOnWSAbort（Phase 4e）：事件 WS 订阅 abort（断开/半开/慢订阅）后，
// 若设备开启 interrupt_on_disconnect 则打断当前 turn；槽空或 finalize 中为 no-op。
// 由 EventLog 异步回调触发，不持任何 core 锁。
func (s *Server) interruptOnWSAbort(id string) {
	s.mu.Lock()
	d, ok := s.devices[id]
	var inst *core.DeviceInstance
	enabled := false
	if ok {
		enabled = d.cfg.Behavior.InterruptOnDisconnect
		inst = d.inst
	}
	s.mu.Unlock()
	if !enabled || inst == nil {
		return
	}
	_, _ = inst.Interrupt("")
}

// refreshServerURLLocked 启动前按环境重解析 url：环境 url 更新后重启生效。
// 删除有引用守卫，解析理论上不会失败；防御性保留旧值。调用方须持 s.mu。
func (s *Server) refreshServerURLLocked(d *managedDevice) {
	if u, err := s.reg.Resolve(d.envName, d.cfg.Enterprise, d.cfg.DeviceType, d.id); err == nil {
		d.cfg.Server.URL = u
	}
}

func (s *Server) spawnInstance(d *managedDevice) *core.DeviceInstance {
	d.log.SetConnGeneration(d.gen)
	id := d.id
	opts := core.Options{
		Dial:               s.opts.Dial,
		Fault:              d.fault,
		InstanceID:         d.instanceID,
		EventLog:           d.log,
		EventLogMaxEntries: s.opts.Config.EventLogMaxEntries,
		Phase2Recording:    true,
		OnTurnTerminal: func(turnID string, ev core.Event) {
			s.onTurnTerminal(id, turnID, ev)
		},
		OnTurnStarted: func(turnID string, uuid uint32, seqBefore int) {
			s.onTurnStarted(id, turnID, uuid, seqBefore)
		},
		OnActivity: func() {
			s.touchActivity(id)
		},
	}
	return core.NewDevice(d.cfg, opts)
}

func (s *Server) runStart(d *managedDevice, inst *core.DeviceInstance, gen int) {
	err := inst.Start(0)
	s.mu.Lock()
	if s.devices[d.id] != d || d.inst != inst {
		s.mu.Unlock()
		return
	}
	if err != nil {
		d.lastError = err.Error()
		d.state = stStopped
		d.committed[gen] = true
		if d.permitHeld {
			d.permitHeld = false
			s.releaseConnLocked()
		}
		s.mu.Unlock()
		return
	}
	d.syncRunning()
	if d.state == stStarting {
		d.state = stRunning
	}
	d.lastActivity = time.Now()
	s.mu.Unlock()

	inst.WaitFinalize()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.devices[d.id] != d || d.inst != inst {
		return
	}
	d.state = stStopped
	d.committed[gen] = true
	if d.permitHeld {
		d.permitHeld = false
		s.releaseConnLocked()
	}
}

func (s *Server) handleWaitReady(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		InstanceID     string   `json:"instance_id"`
		ConnGeneration *int     `json:"conn_generation"`
		TimeoutSec     *float64 `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 非法")
		return
	}
	if body.InstanceID == "" || body.ConnGeneration == nil {
		writeErr(w, http.StatusBadRequest, "缺 instance_id/conn_generation")
		return
	}
	timeout := waitReadyDefault(s.opts.Config)
	if body.TimeoutSec != nil {
		timeout = time.Duration(*body.TimeoutSec * float64(time.Second))
	}
	code, payload := s.waitReady(id, body.InstanceID, *body.ConnGeneration, timeout)
	writeJSON(w, code, payload)
}

func (s *Server) waitReady(deviceID, instanceID string, gen int, timeout time.Duration) (int, any) {
	s.mu.Lock()
	code, payload, inst := s.waitReadyLocked(deviceID, instanceID, gen)
	if code != 0 {
		s.mu.Unlock()
		return code, payload
	}
	if inst == nil {
		s.mu.Unlock()
		return http.StatusGatewayTimeout, map[string]any{"error": "wait_ready 超时"}
	}
	sc, st, ch := inst.OfferSpeakableWait()
	s.mu.Unlock()
	if sc == 200 {
		return http.StatusOK, map[string]any{
			"device_id":        deviceID,
			"instance_id":      instanceID,
			"conn_generation":  gen,
			"connection_state": st.String(),
		}
	}
	if sc == 409 {
		return http.StatusConflict, map[string]any{"error": "generation_gone"}
	}
	if timeout <= 0 {
		timeout = waitReadyDefault(s.opts.Config)
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case res := <-ch:
		if res.Code == 200 {
			return http.StatusOK, map[string]any{
				"device_id":        deviceID,
				"instance_id":      instanceID,
				"conn_generation":  gen,
				"connection_state": res.State.String(),
			}
		}
		return http.StatusConflict, map[string]any{"error": "generation_gone"}
	case <-t.C:
		return s.waitReadyAfterTimeout(deviceID, instanceID, gen, inst, ch)
	}
}

func (s *Server) waitReadyAfterTimeout(deviceID, instanceID string, gen int, inst *core.DeviceInstance, ch chan core.SpeakableResult) (int, any) {
	if inst.RemoveSpeakableWaiter(ch) {
		if inst.FinalizeCommitted() {
			return http.StatusConflict, map[string]any{"error": "generation_gone"}
		}
		return http.StatusGatewayTimeout, map[string]any{"error": "wait_ready 超时"}
	}
	res := <-ch
	if res.Code == 200 {
		return http.StatusOK, map[string]any{
			"device_id":        deviceID,
			"instance_id":      instanceID,
			"conn_generation":  gen,
			"connection_state": res.State.String(),
		}
	}
	return http.StatusConflict, map[string]any{"error": "generation_gone"}
}

// waitReadyLocked 按契约顺序判定。code!=0 表示立即返回。inst 非空表示应登记 waiter。
func (s *Server) waitReadyLocked(deviceID, instanceID string, gen int) (int, any, *core.DeviceInstance) {
	d, liveOK := s.devices[deviceID]
	if liveOK && d.instanceID == instanceID {
		d.syncRunning()
		if d.committed[gen] {
			return http.StatusConflict, map[string]any{"error": "generation_gone"}, nil
		}
		if d.inst != nil && d.inst.FinalizeCommitted() && d.gen == gen {
			d.committed[gen] = true
			return http.StatusConflict, map[string]any{"error": "generation_gone"}, nil
		}
		if d.gen != gen {
			return http.StatusConflict, map[string]any{"error": "generation_gone"}, nil
		}
		if d.inst != nil && core.Speakable(d.inst.ConnectionState(), d.fault) {
			return http.StatusOK, map[string]any{
				"device_id":        deviceID,
				"instance_id":      instanceID,
				"conn_generation":  gen,
				"connection_state": d.connState(),
			}, nil
		}
		return 0, nil, d.inst
	}
	if t, ok := s.tombs[instanceID]; ok && t.deviceID == deviceID && time.Now().Before(t.expires) {
		return http.StatusConflict, map[string]any{"error": "generation_gone"}, nil
	}
	return http.StatusNotFound, map[string]any{"error": "未命中"}, nil
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	code, body := s.stopDevice(id)
	writeJSON(w, code, body)
}

func (s *Server) stopDevice(id string) (int, any) {
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		return http.StatusNotFound, map[string]any{"error": "device 不存在"}
	}
	if d.state == stCreated {
		d.state = stStopped
		ins := d.instanceID
		s.mu.Unlock()
		return http.StatusOK, map[string]any{"device_id": id, "instance_id": ins}
	}
	if d.state == stStopped {
		ins := d.instanceID
		s.mu.Unlock()
		return http.StatusOK, map[string]any{"device_id": id, "instance_id": ins}
	}
	d.state = stStopping
	inst := d.inst
	gen := d.gen
	ins := d.instanceID
	s.mu.Unlock()

	if inst != nil {
		inst.Shutdown()
	}

	s.mu.Lock()
	if s.devices[id] == d {
		d.state = stStopped
		d.committed[gen] = true
		if d.permitHeld {
			d.permitHeld = false
			s.releaseConnLocked()
		}
	}
	s.mu.Unlock()
	return http.StatusOK, map[string]any{"device_id": id, "instance_id": ins}
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	code, body := s.deleteDevice(id)
	writeJSON(w, code, body)
}

func (s *Server) deleteDevice(id string) (int, any) {
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		return http.StatusNotFound, map[string]any{"error": "device 不存在"}
	}
	needStop := d.state == stStarting || d.state == stRunning || d.state == stStopping
	inst := d.inst
	ins := d.instanceID
	s.mu.Unlock()

	if needStop && inst != nil {
		inst.Shutdown()
	}

	s.mu.Lock()
	d, ok = s.devices[id]
	if !ok {
		s.mu.Unlock()
		return http.StatusOK, map[string]any{"device_id": id, "instance_id": ins, "deleted": true}
	}
	if d.permitHeld {
		d.permitHeld = false
		s.releaseConnLocked()
	}
	d.committed[d.gen] = true
	var deletedNotify core.EventNotify
	if d.inst != nil {
		deletedNotify = d.inst.EmitDeleted()
	} else {
		_, deletedNotify = d.log.AppendLocked("device_deleted", "", "", "", "", "")
	}
	s.tombs[d.instanceID] = &tombstone{
		deviceID:   d.id,
		instanceID: d.instanceID,
		expires:    time.Now().Add(s.eventTTL()),
		log:        d.log,
		turns:      d.turns,
		cfg:        d.cfg,
		gen:        d.gen,
	}
	delete(s.devices, id)
	perr := persistWarn("devices.yaml", s.persistDevicesLocked())
	evRec := d.evRec
	d.evRec = nil
	s.mu.Unlock()
	deletedNotify.NotifyHTTP()
	// 在锁外收尾：Stop 会等队列排干，device_deleted 那一行才写得进 events.jsonl。
	evRec.Stop()
	return http.StatusOK, persistErr(map[string]any{"device_id": id, "instance_id": ins, "deleted": true}, perr)
}

type batchBody struct {
	DeviceIDs []string `json:"device_ids"`
	StaggerMs int      `json:"stagger_ms"`
}

type batchOK struct {
	DeviceID       string `json:"device_id"`
	InstanceID     string `json:"instance_id"`
	ConnGeneration int    `json:"conn_generation"`
}

type batchFail struct {
	DeviceID   string `json:"device_id"`
	HTTPStatus int    `json:"http_status"`
	Error      string `json:"error"`
}

func (s *Server) handleBatchStart(w http.ResponseWriter, r *http.Request) {
	var body batchBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	var succ []batchOK
	var fail []batchFail
	stagger := body.StaggerMs
	if stagger == 0 {
		stagger = s.opts.Config.DefaultStaggerMs
	}
	for i, id := range body.DeviceIDs {
		if i > 0 && stagger > 0 {
			time.Sleep(time.Duration(stagger) * time.Millisecond)
		}
		code, ins, gen, errMsg := s.startOne(id)
		if code == http.StatusAccepted {
			succ = append(succ, batchOK{DeviceID: id, InstanceID: ins, ConnGeneration: gen})
		} else {
			fail = append(fail, batchFail{DeviceID: id, HTTPStatus: code, Error: errMsg})
		}
	}
	st := batchHTTPStatus(http.StatusAccepted, succ, fail)
	writeJSON(w, st, map[string]any{"succeeded": succ, "failed": fail})
}

func (s *Server) startOne(id string) (code int, ins string, gen int, errMsg string) {
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		s.mu.Unlock()
		return http.StatusNotFound, "", 0, "device 不存在"
	}
	if d.state != stCreated && d.state != stStopped {
		s.mu.Unlock()
		return http.StatusConflict, d.instanceID, d.gen, "仅 Created/Stopped 可 start"
	}
	if !s.acquireConnLocked() {
		s.mu.Unlock()
		return http.StatusTooManyRequests, d.instanceID, d.gen, "conn_permit"
	}
	s.refreshServerURLLocked(d)
	d.gen++
	d.state = stStarting
	d.permitHeld = true
	d.committed[d.gen] = false
	d.lastActivity = time.Now()
	inst := s.spawnInstance(d)
	d.inst = inst
	gen = d.gen
	ins = d.instanceID
	s.mu.Unlock()
	go s.runStart(d, inst, gen)
	return http.StatusAccepted, ins, gen, ""
}

func (s *Server) handleBatchStop(w http.ResponseWriter, r *http.Request) {
	var body batchBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	var succ []batchOK
	var fail []batchFail
	for _, id := range body.DeviceIDs {
		code, payload := s.stopDevice(id)
		if code == http.StatusOK {
			m, _ := payload.(map[string]any)
			ins, _ := m["instance_id"].(string)
			succ = append(succ, batchOK{DeviceID: id, InstanceID: ins})
		} else {
			fail = append(fail, batchFail{DeviceID: id, HTTPStatus: code, Error: "stop 失败"})
		}
	}
	st := batchHTTPStatus(http.StatusOK, succ, fail)
	writeJSON(w, st, map[string]any{"succeeded": succ, "failed": fail})
}

func (s *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	var body batchBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	var succ []batchOK
	var fail []batchFail
	for _, id := range body.DeviceIDs {
		code, payload := s.deleteDevice(id)
		if code == http.StatusOK {
			m, _ := payload.(map[string]any)
			ins, _ := m["instance_id"].(string)
			succ = append(succ, batchOK{DeviceID: id, InstanceID: ins})
		} else {
			fail = append(fail, batchFail{DeviceID: id, HTTPStatus: code, Error: "delete 失败"})
		}
	}
	st := batchHTTPStatus(http.StatusOK, succ, fail)
	writeJSON(w, st, map[string]any{"succeeded": succ, "failed": fail})
}

func batchHTTPStatus(okCode int, succ []batchOK, fail []batchFail) int {
	if len(fail) == 0 {
		return okCode
	}
	if len(succ) == 0 {
		any429, all409 := false, true
		for _, f := range fail {
			if f.HTTPStatus == http.StatusTooManyRequests {
				any429 = true
			}
			if f.HTTPStatus != http.StatusConflict {
				all409 = false
			}
		}
		if any429 {
			return http.StatusTooManyRequests
		}
		if all409 {
			return http.StatusConflict
		}
		return http.StatusBadRequest
	}
	return http.StatusMultiStatus
}
