package core

import (
	"strings"
	"testing"
)

func TestHandshakeDeviceIsExactlyThreeSegments(t *testing.T) {
	got, err := HandshakeDevice("demo", "A3", "sim_001")
	if err != nil {
		t.Fatal(err)
	}
	if got != "demo/A3/sim_001" {
		t.Fatal(got)
	}
	if n := strings.Count(got, "/") + 1; n != 3 && strings.Count(got, "/") != 2 {
		t.Fatalf("段数不对: %s", got)
	}
	if _, err := HandshakeDevice("demo/x", "A3", "id"); err == nil {
		t.Fatal("段内斜杠应拒绝")
	}
	if HandshakeAction() != "chatbot" {
		t.Fatal()
	}
}
