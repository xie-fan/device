package core

import "testing"

func audio(turn string, uuid uint32, stage, seq uint32) Frame {
	return Frame{Kind: KindAudioData, TurnID: turn, UUID: uuid, Stage: stage, Seq: seq}
}

func TestDataFrameRejectedAtDepthMinusOne(t *testing.T) {
	b := NewOutboundBuffer(2)
	if err := b.EnqueueData(audio("t1", 1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.EnqueueData(Frame{Kind: KindReport}); err != ErrDataFull {
		t.Fatalf("err=%v, 数据在 len>=depth-1 应拒绝", err)
	}
}

func TestStage3AcceptedWhenDataFull(t *testing.T) {
	b := NewOutboundBuffer(2)
	_ = b.EnqueueData(audio("t1", 1, 1, 0))
	got := b.CancelTurn("t1", 1, true)
	if got != CancelEnqueued {
		t.Fatalf("CancelResult=%v", got)
	}
	if b.Len() != 1 {
		t.Fatalf("len=%d, 应只剩 Stage=3（本 Turn 的 1/2 已删）", b.Len())
	}
	if b.Queued()[0].Kind != KindStage3 {
		t.Fatal("应入队 Stage=3")
	}
}

func TestStage3RejectedAtDepth(t *testing.T) {
	b := NewOutboundBuffer(2)
	_ = b.EnqueueData(audio("keep", 9, 1, 0))
	if b.CancelTurn("other", 1, true) != CancelEnqueued {
		t.Fatal("第一帧 Stage=3 应入队")
	}
	if b.CancelTurn("third", 2, true) != CancelBackpressure {
		t.Fatal("len>=depth 时 Stage=3 应 backpressure")
	}
}

func TestStage3SameUUIDIsIdempotent(t *testing.T) {
	b := NewOutboundBuffer(4)
	if b.CancelTurn("t1", 7, true) != CancelEnqueued {
		t.Fatal()
	}
	if b.CancelTurn("t1", 7, true) != CancelEnqueued {
		t.Fatal("同 uuid 未写出 Stage=3 应返回 enqueued 且不追加")
	}
	if b.Len() != 1 {
		t.Fatalf("len=%d", b.Len())
	}
}

func TestCancelTurnDoesNotSetClosing(t *testing.T) {
	b := NewOutboundBuffer(4)
	_ = b.EnqueueData(Frame{Kind: KindACK})
	_ = b.EnqueueData(Frame{Kind: KindReport})
	_ = b.EnqueueData(audio("t1", 1, 1, 0))
	_ = b.EnqueueData(audio("t1", 1, 2, 1))
	if b.CancelTurn("t1", 1, true) != CancelEnqueued {
		t.Fatal()
	}
	if b.Closing() {
		t.Fatal("CancelTurn 禁止置 closing")
	}
	kinds := map[FrameKind]int{}
	for _, f := range b.Queued() {
		kinds[f.Kind]++
	}
	if kinds[KindACK] != 1 || kinds[KindReport] != 1 {
		t.Fatalf("ACK/report 必须保留: %+v", kinds)
	}
	if kinds[KindAudioData] != 0 {
		t.Fatal("本 Turn 未写出的 Stage 1/2 应删除")
	}
	if kinds[KindStage3] != 1 {
		t.Fatal("应追加 Stage=3")
	}
}

func TestCancelTurnReservedTokenNone(t *testing.T) {
	b := NewOutboundBuffer(4)
	_ = b.EnqueueData(audio("t1", 1, 1, 0))
	if b.CancelTurn("t1", 1, false) != CancelNone {
		t.Fatal("Reserved / token=无 应 none")
	}
	if b.Len() != 0 {
		t.Fatal("仍应删掉本 Turn 待发 1/2")
	}
}

func TestInFlightFrameIsNotRetracted(t *testing.T) {
	b := NewOutboundBuffer(4)
	_ = b.EnqueueData(audio("t1", 1, 1, 0))
	_ = b.EnqueueData(audio("t1", 1, 1, 1))
	got, ok := b.TakeForWrite()
	if !ok || got.Seq != 0 {
		t.Fatal(got)
	}
	b.CancelTurn("t1", 1, true)
	if b.inFlight == nil || b.inFlight.Seq != 0 {
		t.Fatal("正在 Write 的帧视为已发出，不得撤回")
	}
	for _, f := range b.Queued() {
		if f.Kind == KindAudioData && f.Seq == 0 {
			t.Fatal("已 Take 的帧不应还在队列里")
		}
	}
}

func TestBeginCloseFiltersKeepStage3(t *testing.T) {
	b := NewOutboundBuffer(8)
	_ = b.EnqueueData(audio("t1", 1, 1, 0))
	_ = b.EnqueueData(Frame{Kind: KindACK})
	_ = b.EnqueueData(Frame{Kind: KindReport})
	if b.CancelTurn("t1", 1, true) != CancelEnqueued {
		t.Fatal()
	}
	res := b.BeginClose(1, "t1", false) // 已 Terminal：token=无
	if res != CloseNone {
		t.Fatalf("CloseResult=%v", res)
	}
	q := b.Queued()
	if len(q) != 1 || q[0].Kind != KindStage3 {
		t.Fatalf("token=无 禁止清空已入队 Stage=3，得到 %+v", q)
	}
	if !b.Closing() {
		t.Fatal("BeginClose 应置 closing")
	}
}

func TestBeginCloseLenUsesKeep(t *testing.T) {
	b := NewOutboundBuffer(2)
	if err := b.EnqueueData(audio("a", 1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if b.CancelTurn("other", 8, true) != CancelEnqueued {
		t.Fatal("数据满时仍应能入一帧 Stage=3")
	}
	// 过滤前 len=2==depth；过滤后 keep 只有 uuid=8 的 Stage=3。
	if b.BeginClose(9, "c", true) != CloseEnqueued {
		t.Fatal("BeginClose 的 len 必须取 keep，不能用过滤前长度")
	}
}

func TestBeginCloseDoesNotEnqueueThenOverwriteKeep(t *testing.T) {
	b := NewOutboundBuffer(4)
	_ = b.EnqueueData(audio("t", 1, 1, 0))
	if b.BeginClose(1, "t", true) != CloseEnqueued {
		t.Fatal()
	}
	for _, f := range b.Queued() {
		if f.Kind == KindAudioData {
			t.Fatal("keep 不得仍含 Stage=1/2")
		}
	}
}
