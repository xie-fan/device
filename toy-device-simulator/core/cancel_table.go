package core

// CancelSendsStage3 对应取消表：Reserved / Terminal 不发；其余发 Stage=3。
func CancelSendsStage3(state TurnState) bool {
	switch state {
	case TurnReserved, TurnTerminal, TurnEmpty:
		return false
	default:
		return true
	}
}

func FinishingUploadDropsUnsentStage2(stage2Sent bool) bool {
	return !stage2Sent
}
