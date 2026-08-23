package core

import (
	"fmt"
	"strings"
	"time"

	"toy-device-simulator/protocol"
)

func (d *DeviceInstance) startRegister() {
	raw, err := protocol.EncodeManage(
		protocol.Topic(d.cfg.Enterprise, d.cfg.DeviceType, d.cfg.DeviceID, "register", "server"),
		protocol.RegisterRequest{
			SequenceNumber:  0,
			Enterprise:      d.cfg.Enterprise,
			DeviceType:      d.cfg.DeviceType,
			DeviceID:        d.cfg.DeviceID,
			FirmwareVersion: d.cfg.FirmwareVersion,
			NicType:         d.cfg.NicType,
			NicICCID:        d.cfg.NicICCID,
		},
	)
	if err != nil {
		d.requestFinalizeAsync("register_encode", false)
		select {
		case d.regDone <- err:
		default:
		}
		return
	}

	d.deviceMu.Lock()
	d.connMu.Lock()
	d.connState = ConnRegistering
	d.registerAttempt++
	attempt := d.registerAttempt
	d.registerOnce = &Once{}
	timeout := seconds(d.cfg.Behavior.RegisterAckTimeoutSec)
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	d.registerTimer = time.AfterFunc(timeout, func() { d.onRegisterTimeout(attempt) })
	d.connMu.Unlock()
	_, n := d.appendEventLocked("registering", "", "", "", "", "")
	d.deviceMu.Unlock()
	d.finishCritical([]EventNotify{n}, TerminalNotify{})

	d.enqueueOrFinalize(Frame{Kind: KindManage, Raw: raw, Topic: protocol.Topic(d.cfg.Enterprise, d.cfg.DeviceType, d.cfg.DeviceID, "register", "server")})
}

func (d *DeviceInstance) onRegisterTimeout(attempt int) {
	d.deviceMu.Lock()
	d.connMu.Lock()
	if attempt != d.registerAttempt || d.registerOnce == nil || !d.registerOnce.TryConsume() {
		d.connMu.Unlock()
		d.deviceMu.Unlock()
		return
	}
	d.connMu.Unlock()
	d.deviceMu.Unlock()
	select {
	case d.regDone <- fmt.Errorf("register ACK 超时"):
	default:
	}
	d.requestFinalizeAsync("register_timeout", false)
}

func (d *DeviceInstance) handleRegisterAckLocked(code int) (acc []EventNotify, asyncFinalize bool) {
	d.connMu.Lock()
	if d.registerOnce == nil || !d.registerOnce.TryConsume() {
		d.connMu.Unlock()
		return nil, false
	}
	if d.registerTimer != nil {
		d.registerTimer.Stop()
	}
	if code == 0 {
		d.connState = ConnRegistered
		d.connMu.Unlock()
		_, n := d.appendEventLocked("registered", "", "", "", "", "")
		select {
		case d.regDone <- nil:
		default:
		}
		return []EventNotify{n}, false
	}
	d.connMu.Unlock()
	_, n := d.appendEventLocked("ack_failure", "", fmt.Sprintf("code=%d", code), "", "", "")
	select {
	case d.regDone <- fmt.Errorf("register ACK code=%d", code):
	default:
	}
	return []EventNotify{n}, true
}

func topicEnds(topic, suffix string) bool {
	return strings.HasSuffix(topic, suffix)
}
