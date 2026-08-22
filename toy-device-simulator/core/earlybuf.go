package core

const EarlyBufCap = 32

type EarlyBuf struct {
	items [][]byte
	drops int
}

func (b *EarlyBuf) Push(raw []byte) (overflow bool) {
	if len(b.items) >= EarlyBufCap {
		b.items = b.items[1:]
		b.drops++
		overflow = true
	}
	b.items = append(b.items, append([]byte(nil), raw...))
	return overflow
}

func (b *EarlyBuf) Drain() [][]byte {
	out := b.items
	b.items = nil
	return out
}

func (b *EarlyBuf) Len() int  { return len(b.items) }
func (b *EarlyBuf) Drops() int { return b.drops }
