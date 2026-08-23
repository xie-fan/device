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

func (s ConnState) String() string {
	switch s {
	case ConnDisconnected:
		return "disconnected"
	case ConnConnecting:
		return "connecting"
	case ConnConnected:
		return "connected"
	case ConnRegistering:
		return "registering"
	case ConnRegistered:
		return "registered"
	case ConnReporting:
		return "reporting"
	case ConnReady:
		return "ready"
	case ConnDisconnecting:
		return "disconnecting"
	default:
		return "unknown"
	}
}

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
