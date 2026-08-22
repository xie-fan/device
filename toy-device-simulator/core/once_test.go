package core

import "testing"

func TestRegisterAckAndTimeoutConsumeOnlyOnce(t *testing.T) {
	var o Once
	if !o.TryConsume() {
		t.Fatal("第一次应成功")
	}
	if o.TryConsume() {
		t.Fatal("ACK 与 timeout 只能消费一次")
	}
}

func TestTimerStopDoesNotReplaceConsume(t *testing.T) {
	var o Once
	stopped := true // 模拟 Timer.Stop 返回 true 但仍可能有已开火的 callback
	_ = stopped
	if !o.TryConsume() {
		t.Fatal("必须以 try_consume 为准")
	}
	if o.TryConsume() {
		t.Fatal("Stop 成功也不能再消费")
	}
}
