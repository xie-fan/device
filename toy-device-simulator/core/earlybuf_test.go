package core

import "testing"

func TestEarlyBufCapacity32DropsOldest(t *testing.T) {
	var b EarlyBuf
	for i := 0; i < EarlyBufCap; i++ {
		if b.Push([]byte{byte(i)}) {
			t.Fatalf("第 %d 次不应溢出", i)
		}
	}
	if !b.Push([]byte{0xff}) {
		t.Fatal("第 33 条应溢出并记 early_downlink_overflow")
	}
	if b.Drops() != 1 || b.Len() != EarlyBufCap {
		t.Fatalf("drops=%d len=%d", b.Drops(), b.Len())
	}
	if b.Drain()[0][0] != 1 {
		t.Fatal("应丢掉最旧的 0")
	}
	if len(b.Drain()) != 0 {
		t.Fatal("回放只允许一次：第二次 Drain 为空，因此不会二次 ACK/录帧")
	}
}
