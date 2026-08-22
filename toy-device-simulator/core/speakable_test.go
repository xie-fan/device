package core

import "testing"

func TestSpeakableByFault(t *testing.T) {
	cases := []struct {
		fault Fault
		conn  ConnState
		want  bool
	}{
		{FaultNone, ConnReady, true},
		{FaultNone, ConnRegistered, false},
		{FaultSkipRegister, ConnConnected, true},
		{FaultSkipRegister, ConnReady, false},
		{FaultSkipReport, ConnRegistered, true},
		{FaultSkipReport, ConnReady, false},
		{FaultNone, ConnConnecting, false},
	}
	for _, tc := range cases {
		if got := Speakable(tc.conn, tc.fault); got != tc.want {
			t.Fatalf("fault=%s conn=%v got=%v want=%v", tc.fault, tc.conn, got, tc.want)
		}
	}
}
