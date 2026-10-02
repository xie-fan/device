package core

import (
	"fmt"
	"time"

	"toy-device-simulator/protocol"
)

// SendTrans 经 WS 上行一条 '3' 转发消息（对齐基线 trans.go：设备经 WS 调 OpenAPI）。
// 仅 Ready 可发；下行回包走 handleTransDownlink 记 trans_response 事件。
func (d *DeviceInstance) SendTrans(eventType, path string, header, body map[string]any) error {
	d.deviceMu.Lock()
	d.connMu.Lock()
	st := d.connState
	d.connMu.Unlock()
	if st != ConnReady || d.finalizeStarted || d.finalizeCommitted || d.deleted {
		d.deviceMu.Unlock()
		return fmt.Errorf("仅 Ready 可 POST /trans")
	}
	var data protocol.TransferData
	data.DeviceID = d.cfg.DeviceID
	data.Enterprise = d.cfg.Enterprise
	data.DeviceType = d.cfg.DeviceType
	data.EventType = eventType
	data.Timestamp = time.Now().Unix()
	data.Request.Path = path
	data.Request.Header = header
	data.Request.Body = body
	raw, err := protocol.EncodeTransFrame(data)
	if err != nil {
		d.deviceMu.Unlock()
		return err
	}
	_, n := d.appendEventLocked("trans_request", "", path, "", "", "")
	d.deviceMu.Unlock()
	d.finishCritical([]EventNotify{n}, TerminalNotify{})
	d.enqueueOrFinalize(Frame{Kind: KindManage, Raw: raw})
	return nil
}
