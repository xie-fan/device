package core

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"toy-device-simulator/config"
)

func RecordingDir(outputDir, deviceID, turnID string) (string, error) {
	if err := config.ValidatePathComponent(deviceID); err != nil {
		return "", fmt.Errorf("device_id: %w", err)
	}
	if err := config.ValidatePathComponent(turnID); err != nil {
		return "", fmt.Errorf("turn_id: %w", err)
	}
	base := filepath.Clean(outputDir)
	dir := filepath.Clean(filepath.Join(outputDir, deviceID, turnID))
	if !underOutputDir(base, dir) {
		return "", fmt.Errorf("录制目录逃出 output_dir")
	}
	return dir, nil
}

func RecordingPaths(outputDir, deviceID, turnID string) (frames, uplink, downlink, turn string, err error) {
	dir, err := RecordingDir(outputDir, deviceID, turnID)
	if err != nil {
		return "", "", "", "", err
	}
	return filepath.Join(dir, "frames.jsonl"),
		filepath.Join(dir, "uplink.pcm"),
		filepath.Join(dir, "downlink.pcm"),
		filepath.Join(dir, "turn.json"),
		nil
}

// RecordingDirPhase2 为 recordings/{device_id}/{instance_id}/{turn_id}/。
func RecordingDirPhase2(outputDir, deviceID, instanceID, turnID string) (string, error) {
	if err := config.ValidatePathComponent(deviceID); err != nil {
		return "", fmt.Errorf("device_id: %w", err)
	}
	if err := config.ValidatePathComponent(instanceID); err != nil {
		return "", fmt.Errorf("instance_id: %w", err)
	}
	if err := config.ValidatePathComponent(turnID); err != nil {
		return "", fmt.Errorf("turn_id: %w", err)
	}
	base := filepath.Clean(outputDir)
	dir := filepath.Clean(filepath.Join(outputDir, deviceID, instanceID, turnID))
	if !underOutputDir(base, dir) {
		return "", fmt.Errorf("录制目录逃出 output_dir")
	}
	return dir, nil
}

// RecordingDeviceDir 为 recordings/{device_id}/，其下每个子目录是一次运行。
func RecordingDeviceDir(outputDir, deviceID string) (string, error) {
	if err := config.ValidatePathComponent(deviceID); err != nil {
		return "", fmt.Errorf("device_id: %w", err)
	}
	base := filepath.Clean(outputDir)
	dir := filepath.Clean(filepath.Join(outputDir, deviceID))
	if !underOutputDir(base, dir) {
		return "", fmt.Errorf("录制目录逃出 output_dir")
	}
	return dir, nil
}

func RecordingInstanceDir(outputDir, deviceID, instanceID string) (string, error) {
	if err := config.ValidatePathComponent(deviceID); err != nil {
		return "", fmt.Errorf("device_id: %w", err)
	}
	if err := config.ValidatePathComponent(instanceID); err != nil {
		return "", fmt.Errorf("instance_id: %w", err)
	}
	base := filepath.Clean(outputDir)
	dir := filepath.Clean(filepath.Join(outputDir, deviceID, instanceID))
	if !underOutputDir(base, dir) {
		return "", fmt.Errorf("录制目录逃出 output_dir")
	}
	return dir, nil
}

func RecordingPathsPhase2(outputDir, deviceID, instanceID, turnID string) (frames, uplink, downlink, turn string, err error) {
	dir, err := RecordingDirPhase2(outputDir, deviceID, instanceID, turnID)
	if err != nil {
		return "", "", "", "", err
	}
	return filepath.Join(dir, "frames.jsonl"),
		filepath.Join(dir, "uplink.pcm"),
		filepath.Join(dir, "downlink.pcm"),
		filepath.Join(dir, "turn.json"),
		nil
}

// underOutputDir 要求 Clean 后的 target 仍是 base 的子路径。
func underOutputDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(rel, ".."+sep) {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

func PathHasInstanceID(p, instanceID string) bool {
	if instanceID == "" {
		return false
	}
	return strings.Contains(filepath.ToSlash(p), "/"+instanceID+"/") ||
		strings.Contains(path.Clean(p), instanceID)
}
