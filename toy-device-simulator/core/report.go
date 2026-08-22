package core

type ReportSeq struct {
	next    int
	pending map[int]struct{}
}

func NewReportSeq(start int) *ReportSeq {
	return &ReportSeq{next: start, pending: map[int]struct{}{}}
}

func (r *ReportSeq) TakeLocked() int {
	n := r.next
	r.next++
	r.pending[n] = struct{}{}
	return n
}

func (r *ReportSeq) Ack(seq int) (matched bool) {
	if _, ok := r.pending[seq]; !ok {
		return false
	}
	delete(r.pending, seq)
	return true
}

func KeepaliveEnabled(conn ConnState, skipReport bool) bool {
	return conn == ConnReady && !skipReport
}
