package core

import "testing"

func TestWSLiveInboxOverflowAborts(t *testing.T) {
	log := NewEventLog("sim_hub", "ins_hub")
	sub, ok := log.RegisterWS(0, "")
	if !ok || sub == nil {
		t.Fatal("RegisterWS 应成功")
	}
	_, abort, drain, live := log.CatchupWS(sub)
	if abort || drain || !live {
		t.Fatalf("空 backlog catchup 应立即 live，abort=%v drain=%v live=%v", abort, drain, live)
	}
	for i := 0; i < 300; i++ {
		log.AppendLocked("ready", "", "", "", "", "")
	}
	_, abort, _, _ = log.PollWSLive(sub)
	if !abort {
		t.Fatal("live inbox 超过 256 应 abort")
	}
}
