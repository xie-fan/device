package core

type ConnState int

const (
	ConnDisconnected ConnState = iota
	ConnConnecting
	ConnConnected
	ConnRegistering
	ConnRegistered
	ConnReporting
	ConnReady
	ConnDisconnecting
)

func Speakable(conn ConnState, fault Fault) bool {
	switch fault {
	case FaultSkipRegister:
		return conn == ConnConnected
	case FaultSkipReport:
		return conn == ConnRegistered
	default:
		return conn == ConnReady
	}
}
