package core

import (
	"path"
	"path/filepath"
	"strings"
)

func RecordingDir(outputDir, deviceID, turnID string) string {
	return filepath.Join(outputDir, deviceID, turnID)
}

func RecordingPaths(outputDir, deviceID, turnID string) (frames, uplink, downlink, turn string) {
	dir := RecordingDir(outputDir, deviceID, turnID)
	return filepath.Join(dir, "frames.jsonl"),
		filepath.Join(dir, "uplink.pcm"),
		filepath.Join(dir, "downlink.pcm"),
		filepath.Join(dir, "turn.json")
}

func PathHasInstanceID(p, instanceID string) bool {
	if instanceID == "" {
		return false
	}
	return strings.Contains(filepath.ToSlash(p), "/"+instanceID+"/") ||
		strings.Contains(path.Clean(p), instanceID)
}
