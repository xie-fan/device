package scenario

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Step 是 Scenario 的一步。
type Step struct {
	Action        string          `json:"action"`
	DeviceIDs     []string        `json:"device_ids"`
	DeviceID      string          `json:"device_id"`
	InstanceID    string          `json:"instance_id"`
	AssetID       string          `json:"asset_id"`
	EventType     string          `json:"event_type"`
	TurnID        string          `json:"turn_id"`
	Wait          bool            `json:"wait"`
	WaitReady     *bool           `json:"wait_ready"`
	AfterEventSeq json.RawMessage `json:"after_event_seq"`
	StaggerMs     int             `json:"stagger_ms"`
}

type Spec struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

func ApplyDefaults(s *Step) {
	if s == nil {
		return
	}
	if s.Action == "batch_start" && s.WaitReady == nil {
		v := true
		s.WaitReady = &v
	}
}

func ValidateAssert(s Step) error {
	if len(s.AfterEventSeq) == 0 || string(s.AfterEventSeq) == "null" {
		return errors.New("assert 必须带 after_event_seq")
	}
	return nil
}

// ResolveAfterSeq 解析 after_event_seq，支持数字与 $prev.seq_before。
func ResolveAfterSeq(raw json.RawMessage, prevSeqBefore int) (int, error) {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, `"`)
	if s == "$prev.seq_before" {
		return prevSeqBefore, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		var n2 int
		if err2 := json.Unmarshal(raw, &n2); err2 != nil {
			return 0, fmt.Errorf("after_event_seq 非法")
		}
		return n2, nil
	}
	return n, nil
}

func SubstPrev(s, prevInstance, prevTurn string) string {
	s = strings.ReplaceAll(s, "$prev.instance_id", prevInstance)
	s = strings.ReplaceAll(s, "$prev.turn_id", prevTurn)
	return s
}

func BatchStartWaitsReady(s Step) bool {
	if s.WaitReady != nil {
		return *s.WaitReady
	}
	return true
}

func Start(spec []byte) (runID string, err error) {
	var s Spec
	if err := json.Unmarshal(spec, &s); err != nil {
		return "", err
	}
	for i := range s.Steps {
		ApplyDefaults(&s.Steps[i])
		if s.Steps[i].Action == "assert" {
			if err := ValidateAssert(s.Steps[i]); err != nil {
				return "", err
			}
		}
	}
	id, err := newRunID()
	if err != nil {
		return "", err
	}
	return id, nil
}

func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("run_%s", hex.EncodeToString(b[:])), nil
}
