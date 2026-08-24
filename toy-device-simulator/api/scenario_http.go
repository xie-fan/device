package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"toy-device-simulator/scenario"
)

func (s *Server) handleScenarioRun(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取失败")
		return
	}
	runID, err := scenario.Start(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var spec scenario.Spec
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &spec); err != nil {
			writeErr(w, http.StatusBadRequest, "JSON 非法")
			return
		}
	}
	for i := range spec.Steps {
		scenario.ApplyDefaults(&spec.Steps[i])
	}
	steps := make([]stepResult, len(spec.Steps))
	for i := range steps {
		steps[i] = stepResult{Index: i, Status: "pending"}
	}
	s.mu.Lock()
	s.runs[runID] = &scenarioRun{ID: runID, Status: "running", Steps: steps}
	s.mu.Unlock()
	go s.execScenario(runID, spec)
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

func (s *Server) handleScenarioGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	run, ok := s.runs[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "run 不存在")
		return
	}
	snap := cloneScenarioRun(run)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, snap)
}

func cloneScenarioRun(run *scenarioRun) scenarioRun {
	out := scenarioRun{ID: run.ID, Status: run.Status}
	if run.Steps != nil {
		out.Steps = make([]stepResult, len(run.Steps))
		copy(out.Steps, run.Steps)
	}
	return out
}

func (s *Server) execScenario(runID string, spec scenario.Spec) {
	var prev stepResult
	failed := false
	for i := range spec.Steps {
		st := spec.Steps[i]
		res := stepResult{Index: i, Status: "running"}
		s.patchRunStep(runID, i, res)
		if failed {
			res.Status = "skipped"
			s.patchRunStep(runID, i, res)
			continue
		}
		out, err := s.execStep(st, prev)
		if out.ConnGeneration == 0 {
			out.ConnGeneration = prev.ConnGeneration
		}
		if out.InstanceID == "" {
			out.InstanceID = prev.InstanceID
		}
		if err != nil {
			out.Index = i
			out.Status = "failed"
			out.Error = err.Error()
			s.patchRunStep(runID, i, out)
			failed = true
			prev = out
			continue
		}
		out.Index = i
		out.Status = "succeeded"
		s.patchRunStep(runID, i, out)
		prev = out
	}
	status := "succeeded"
	if failed {
		status = "failed"
	}
	s.mu.Lock()
	if run, ok := s.runs[runID]; ok {
		run.Status = status
	}
	s.mu.Unlock()
}

func (s *Server) patchRunStep(runID string, i int, res stepResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || i < 0 || i >= len(run.Steps) {
		return
	}
	run.Steps[i] = res
}

func (s *Server) execStep(st scenario.Step, prev stepResult) (stepResult, error) {
	st.DeviceID = scenario.SubstPrev(st.DeviceID, prev.InstanceID, prev.TurnID)
	st.InstanceID = scenario.SubstPrev(st.InstanceID, prev.InstanceID, prev.TurnID)
	st.TurnID = scenario.SubstPrev(st.TurnID, prev.InstanceID, prev.TurnID)
	st.AssetID = scenario.SubstPrev(st.AssetID, prev.InstanceID, prev.TurnID)
	switch st.Action {
	case "batch_start":
		return s.execBatchStart(st)
	case "speak":
		return s.execSpeak(st)
	case "assert", "wait":
		return s.execAssert(st, prev)
	default:
		return stepResult{}, fmt.Errorf("未知 action %s", st.Action)
	}
}

func (s *Server) execBatchStart(st scenario.Step) (stepResult, error) {
	body := map[string]any{"device_ids": st.DeviceIDs, "stagger_ms": st.StaggerMs}
	code, raw := s.internalJSON(http.MethodPost, "/devices/batch/start", body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	if code != http.StatusAccepted && code != http.StatusMultiStatus && code != http.StatusOK {
		return stepResult{}, fmt.Errorf("batch_start HTTP %d %s", code, raw)
	}
	out := stepResult{}
	if succ, ok := m["succeeded"].([]any); ok {
		for _, row := range succ {
			rm, _ := row.(map[string]any)
			id := jsonStr(rm, "device_id")
			ins := jsonStr(rm, "instance_id")
			gen := jsonInt(rm, "conn_generation")
			out.InstanceID = ins
			out.ConnGeneration = gen
			if scenario.BatchStartWaitsReady(st) && id != "" && ins != "" {
				wcode, wbody := s.waitReady(id, ins, gen, waitReadyDefault(s.opts.Config))
				if wcode != http.StatusOK {
					return out, fmt.Errorf("wait_ready %s HTTP %d %v", id, wcode, wbody)
				}
			}
		}
	}
	if fail, ok := m["failed"].([]any); ok && len(fail) > 0 && out.InstanceID == "" {
		return out, fmt.Errorf("batch_start 失败 %s", raw)
	}
	return out, nil
}

func (s *Server) execSpeak(st scenario.Step) (stepResult, error) {
	path := "/devices/" + st.DeviceID + "/speak"
	body := map[string]any{"asset_id": st.AssetID}
	if st.Wait {
		path = "/devices/" + st.DeviceID + "/speak_and_wait"
		body["timeout_sec"] = waitReadyDefault(s.opts.Config).Seconds()
	}
	code, raw := s.internalJSON(http.MethodPost, path, body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	out := stepResult{
		InstanceID:     jsonStr(m, "instance_id"),
		TurnID:         jsonStr(m, "turn_id"),
		SeqBefore:      jsonInt(m, "seq_before"),
		ConnGeneration: jsonInt(m, "conn_generation"),
	}
	if out.ConnGeneration == 0 {
		_, out.ConnGeneration = s.liveConnMeta(st.DeviceID, out.InstanceID)
	}
	want := http.StatusAccepted
	if st.Wait {
		want = http.StatusOK
	}
	if code != want {
		return out, fmt.Errorf("speak HTTP %d %s", code, raw)
	}
	return out, nil
}

func (s *Server) execAssert(st scenario.Step, prev stepResult) (stepResult, error) {
	if err := scenario.ValidateAssert(st); err != nil {
		return stepResult{}, err
	}
	after, err := scenario.ResolveAfterSeq(st.AfterEventSeq, prev.SeqBefore)
	if err != nil {
		return stepResult{}, err
	}
	ins := st.InstanceID
	if ins == "" {
		ins = prev.InstanceID
	}
	turnID := st.TurnID
	gen := prev.ConnGeneration
	if _, g := s.liveConnMeta(st.DeviceID, ins); g != 0 {
		gen = g
	}
	body := map[string]any{
		"device_id":       st.DeviceID,
		"instance_id":     ins,
		"event_type":      st.EventType,
		"turn_id":         turnID,
		"after_event_seq": after,
		"timeout_sec":     waitReadyDefault(s.opts.Config).Seconds(),
	}
	if gen != 0 {
		body["conn_generation"] = gen
	}
	code, raw := s.internalJSON(http.MethodPost, "/wait", body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	out := stepResult{
		InstanceID:     ins,
		TurnID:         jsonStr(m, "turn_id"),
		SeqBefore:      after,
		ConnGeneration: gen,
	}
	if code != http.StatusOK {
		return out, fmt.Errorf("assert HTTP %d %s", code, raw)
	}
	return out, nil
}

func (s *Server) liveConnMeta(deviceID, instanceID string) (ins string, gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[deviceID]
	if !ok {
		return instanceID, 0
	}
	if instanceID != "" && d.instanceID != instanceID {
		return instanceID, 0
	}
	return d.instanceID, d.gen
}

func jsonStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func jsonInt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func (s *Server) internalJSON(method, path string, body any) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return http.StatusBadRequest, []byte(err.Error())
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}
