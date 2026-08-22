package fixture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type Line struct {
	RunID    string `json:"run_id"`
	N        int    `json:"n"`
	DeviceID string `json:"device_id"`
}

func DeviceID(runID string, n int) string {
	return fmt.Sprintf("sim_sr_%s_%d", runID, n)
}

func NextN(runID string, existing []Line) int {
	max := 0
	for _, l := range existing {
		if l.RunID == runID && l.N > max {
			max = l.N
		}
	}
	return max + 1
}

func Alloc(runID string, existing []Line) Line {
	n := NextN(runID, existing)
	return Line{RunID: runID, N: n, DeviceID: DeviceID(runID, n)}
}

func ReadJSONL(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Line
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(line, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

func AppendJSONL(path string, l Line) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
