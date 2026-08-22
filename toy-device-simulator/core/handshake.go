package core

import (
	"fmt"
	"strings"
)

func HandshakeDevice(enterprise, deviceType, deviceID string) (string, error) {
	if enterprise == "" || deviceType == "" || deviceID == "" {
		return "", fmt.Errorf("Device 三段均不能为空")
	}
	if strings.Contains(enterprise, "/") || strings.Contains(deviceType, "/") || strings.Contains(deviceID, "/") {
		return "", fmt.Errorf("Device 各段不得含 /")
	}
	return enterprise + "/" + deviceType + "/" + deviceID, nil
}

func HandshakeAction() string { return "chatbot" }
