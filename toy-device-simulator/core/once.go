package core

import "sync/atomic"

// Once 用于 register ACK∥timeout 一次性消费。Timer.Stop 不能代替它。
type Once struct{ done atomic.Bool }

func (o *Once) TryConsume() bool {
	return o.done.CompareAndSwap(false, true)
}
