package core

import "testing"

func TestFirstReportSeqEqualsStart(t *testing.T) {
	r := NewReportSeq(7)
	if n := r.TakeLocked(); n != 7 {
		t.Fatalf("got %d", n)
	}
	if n := r.TakeLocked(); n != 8 {
		t.Fatalf("keepalive 共用计数器, got %d", n)
	}
}

func TestReportEchoMatchAndUnmatched(t *testing.T) {
	r := NewReportSeq(1)
	seq := r.TakeLocked()
	if !r.Ack(seq) {
		t.Fatal("应命中 pending")
	}
	if r.Ack(seq) {
		t.Fatal("重复回显应 unmatched")
	}
}

func TestSkipReportDisablesKeepalive(t *testing.T) {
	if KeepaliveEnabled(ConnRegistered, true) {
		t.Fatal("skip_report 不停 Ready，也不发 keepalive")
	}
	if !KeepaliveEnabled(ConnReady, false) {
		t.Fatal("Ready 后应发周期 report")
	}
}
