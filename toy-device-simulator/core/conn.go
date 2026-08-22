package core

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"toy-device-simulator/protocol"
	"toy-device-simulator/recording"

	"github.com/gorilla/websocket"
)

func DialGorilla(url string, header http.Header) (Conn, error) {
	c, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (d *DeviceInstance) Start(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)

	d.deviceMu.Lock()
	d.connMu.Lock()
	d.connState = ConnConnecting
	d.connMu.Unlock()
	d.deviceMu.Unlock()

	dev, err := HandshakeDevice(d.cfg.Enterprise, d.cfg.DeviceType, d.cfg.DeviceID)
	if err != nil {
		d.RequestFinalize("dial", false)
		d.WaitFinalize()
		return err
	}
	h := make(http.Header)
	h.Set("Device", dev)
	h.Set("Action", HandshakeAction())

	conn, err := d.dial(d.cfg.Server.URL, h)
	if err != nil {
		d.RequestFinalize("dial", false)
		d.WaitFinalize()
		return fmt.Errorf("Dial 失败: %w", err)
	}

	d.deviceMu.Lock()
	d.connMu.Lock()
	d.conn = conn
	d.connGeneration = 1
	d.connState = ConnConnected
	d.connMu.Unlock()
	d.appendEventLocked("connected", "", "", "", "", "")
	d.readLoopDone = make(chan struct{})
	d.deviceMu.Unlock()

	go d.readLoop(conn)
	go d.writePump(conn)

	if d.fault == FaultSkipRegister {
		return nil
	}

	d.startRegister()
	if err := waitErr(d.regDone, d.finalizeDone, time.Until(deadline), "等待 register ACK"); err != nil {
		d.RequestFinalize("register", false)
		d.WaitFinalize()
		return err
	}

	if d.fault == FaultSkipReport {
		return nil
	}

	d.sendReport("initial")
	if err := waitErr(d.readyDone, d.finalizeDone, time.Until(deadline), "等待初始 report 回显"); err != nil {
		d.RequestFinalize("report", false)
		d.WaitFinalize()
		return err
	}
	d.startKeepalive()
	return nil
}

func waitErr(ch <-chan error, done <-chan struct{}, timeout time.Duration, what string) error {
	if timeout < 0 {
		timeout = 0
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-ch:
		return err
	case <-done:
		return fmt.Errorf("%s：连接已收口", what)
	case <-t.C:
		return fmt.Errorf("%s 超时", what)
	}
}

func (d *DeviceInstance) readLoop(conn Conn) {
	defer close(d.readLoopDone)
	for {
		_, p, err := conn.ReadMessage()
		if err != nil {
			d.requestFinalizeAsync("connection_lost", false)
			return
		}
		raw := append([]byte(nil), p...)
		d.handleInbound(raw)
	}
}

func (d *DeviceInstance) writePump(conn Conn) {
	for {
		d.writePumpMu.Lock()
		for d.outbound.Len() == 0 && !d.outbound.Closing() {
			d.writePumpCond.Wait()
		}
		if d.outbound.Len() == 0 {
			d.writePumpMu.Unlock()
			return
		}
		f, ok := d.outbound.TakeForWrite()
		d.writePumpMu.Unlock()
		if !ok {
			continue
		}

		_ = conn.SetWriteDeadline(time.Now().Add(d.drainTimeout))
		err := conn.WriteMessage(websocket.TextMessage, f.Raw)

		d.writePumpMu.Lock()
		d.outbound.WriteDone()
		d.writePumpCond.Broadcast()
		d.writePumpMu.Unlock()

		if err != nil {
			d.requestFinalizeAsync("write", false)
			return
		}
		d.onWritten(f)
	}
}

func (d *DeviceInstance) onWritten(f Frame) {
	now := time.Now()
	tr := f.turn
	if tr == nil {
		return
	}
	d.recorder.SubmitFrame(tr.framesPath, recording.RowFromRaw("outbound", f.Raw, f.InjectedFault, now))
	if f.Kind == KindAudioData && f.Stage == protocol.StageUploading {
		view := protocol.Inspect(f.Raw)
		d.recorder.SubmitPCM(tr.upPath, view.Payload, true)
	}
	if f.Kind == KindAudioData && f.Stage == protocol.StageFinished {
		go d.noteStage2Sent(f.TurnID)
	}
	if f.Kind == KindAudioData && (f.Stage == protocol.StageUploading || f.Stage == protocol.StageFinished) {
		if tr.out.Add(-1) <= 0 {
			tr.signalDrained()
		}
	}
}

func (d *DeviceInstance) noteStage2Sent(turnID string) {
	d.deviceMu.Lock()
	defer d.deviceMu.Unlock()
	if d.slot.ID() != turnID {
		return
	}
	d.slot.SetUplinkEnd("stage2")
}

func (d *DeviceInstance) enqueueData(f Frame) error {
	d.writePumpMu.Lock()
	defer d.writePumpMu.Unlock()
	err := d.outbound.EnqueueData(f)
	if err == nil {
		d.writePumpCond.Signal()
	}
	return err
}

func (d *DeviceInstance) enqueueOrFinalize(f Frame) {
	if err := d.enqueueData(f); err != nil {
		if errors.Is(err, ErrDataFull) {
			d.requestFinalizeAsync("write_backpressure", false)
		}
	}
}

func audioOpenCount(b *OutboundBuffer, turnID string) int {
	n := b.CountQueuedAudio(turnID)
	if inf := b.InFlight(); inf != nil && inf.TurnID == turnID && inf.Kind == KindAudioData && (inf.Stage == 1 || inf.Stage == 2) {
		n++
	}
	return n
}
