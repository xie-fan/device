package core

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

func TestPhaseCStopsPendingReportTimer(t *testing.T) {
	cfg := testDeviceCfg(t)
	cfg.Behavior.ReportEchoTimeoutSec = 1
	conn := NewFakeConn()
	var mu sync.Mutex
	echoReport := true
	go func() {
		for {
			select {
			case msg := <-conn.writeCh:
				if len(msg) == 0 || msg[0] != protocol.FirstManage {
					continue
				}
				env, err := protocol.DecodeManage(msg)
				if err != nil {
					continue
				}
				if topicEnds(env.Topic, "/register/server") {
					ack, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", protocol.RegisterAck{Code: 0})
					conn.Push(ack)
				}
				if topicEnds(env.Topic, "/report/server") {
					mu.Lock()
					ok := echoReport
					mu.Unlock()
					if !ok {
						continue
					}
					echo, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", env.Data)
					conn.Push(echo)
				}
			case <-conn.closed:
				return
			}
		}
	}()
	d := NewDevice(cfg, Options{
		Dial: func(string, http.Header) (Conn, error) { return conn, nil },
	})
	t.Cleanup(func() { d.Shutdown() })
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	echoReport = false
	mu.Unlock()
	if _, err := d.ManualReport(nil); err != nil {
		t.Fatal(err)
	}
	d.Shutdown()
	n := d.EmitDeleted()
	n.NotifyHTTP()
	time.Sleep(1500 * time.Millisecond)
	seenDeleted := false
	for _, ev := range d.Events() {
		if ev.Type == "device_deleted" {
			seenDeleted = true
		}
		if ev.Type == "report_timeout" && seenDeleted {
			t.Fatal("device_deleted 之后不得再写 report_timeout")
		}
		if ev.Type == "report_timeout" {
			t.Fatal("Phase C 停掉 pending timer 后不得写 report_timeout")
		}
	}
}

func TestManualReportAfterFinalizeFails(t *testing.T) {
	cfg := testDeviceCfg(t)
	d, _ := newTestDevice(t, cfg, FaultNone, autoOpts{})
	if err := d.Start(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	d.Shutdown()
	if _, err := d.ManualReport(nil); err == nil {
		t.Fatal("finalize 后 ManualReport 不得成功（不得 202）")
	}
}
