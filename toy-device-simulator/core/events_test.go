package core

import "testing"

func TestEventSeqStartsAtOneAndAccumulates(t *testing.T) {
	log := NewEventLog("sim_001", "ins_test")
	var acc []EventNotify
	_, n1 := log.AppendLocked("connected", "", "", "", "", "")
	acc = append(acc, n1)
	ev, n2 := log.AppendLocked("turn_terminal", "t1", "", EndIdle, "stage2", ReplyTTS)
	acc = append(acc, n2)
	if ev.EventSeq != 2 || ev.DeviceID != "sim_001" || ev.InstanceID != "ins_test" {
		t.Fatalf("%+v", ev)
	}
	if ev.TurnID != "t1" || ev.EndReason != EndIdle || ev.ReplyKind != ReplyTTS {
		t.Fatal("turn_terminal 必带终态字段")
	}
	if n1.EventWaiters != 0 || n2.SlowSubs != 0 {
		t.Fatal("Phase 1 waiter/hub 应为空")
	}
	if len(acc) != 2 {
		t.Fatal("同一临界区的 EventNotify 必须累加，不得丢中间返回值")
	}
}

func TestAppendLockedPicksRegisteredWaiter(t *testing.T) {
	log := NewEventLog("sim_001", "ins_w")
	_, ch, expired, hit := log.FindOrRegisterWaiter(0, "ready", "")
	if expired || hit || ch == nil {
		t.Fatal("无人命中历史时应登记 waiter")
	}
	if log.WaiterCount() != 1 {
		t.Fatalf("WaiterCount=%d", log.WaiterCount())
	}
	ev, n := log.AppendLocked("ready", "", "", "", "", "")
	if n.EventWaiters != 1 {
		t.Fatalf("登记后 AppendLocked 必须摘走 waiter, EventWaiters=%d", n.EventWaiters)
	}
	if log.WaiterCount() != 0 {
		t.Fatal("摘走后表应空")
	}
	n.NotifyHTTP()
	select {
	case got := <-ch:
		if got.EventSeq != ev.EventSeq || got.Type != "ready" {
			t.Fatalf("%+v", got)
		}
	default:
		t.Fatal("摘走的 waiter 必须被唤醒")
	}
}

func TestForbiddenServerLogNames(t *testing.T) {
	for _, name := range []string{"device_not_found", "status_invalid", "no_active_turn", "inferred_no_reply"} {
		if !ForbiddenEventType(name) {
			t.Fatalf("%s 禁止发出", name)
		}
	}
}
