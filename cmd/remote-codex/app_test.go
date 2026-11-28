package main

import (
	"context"
	"os"
	"testing"
)

func TestHostProbeConsecutiveFailures(t *testing.T) {
	a := &application{}
	for failure := 1; failure <= 3; failure++ {
		if reconnect := a.shouldReconnectHost(context.DeadlineExceeded, nil, false); reconnect != (failure == 3) {
			t.Fatalf("failure %d: reconnect = %v", failure, reconnect)
		}
	}
	if a.shouldReconnectHost(nil, nil, false) || a.hostProbeFailures != 0 {
		t.Fatal("successful probe must reset consecutive failures")
	}
	for attempt := 0; attempt < 3; attempt++ {
		if a.shouldReconnectHost(context.DeadlineExceeded, nil, false) {
			t.Fatal("isolated timeout must preserve the host")
		}
		if a.shouldReconnectHost(nil, nil, false) || a.hostProbeFailures != 0 {
			t.Fatal("successful probe must reset consecutive failures")
		}
	}
}

func TestHostProbeImmediateReconnect(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "archive_changed", true: "host_offline"}[offline], func(t *testing.T) {
			a := &application{}
			if a.shouldReconnectHost(context.DeadlineExceeded, nil, false) {
				t.Fatal("first timeout must preserve the host")
			}
			var archiveErr error
			if !offline {
				archiveErr = os.ErrNotExist
			}
			if !a.shouldReconnectHost(nil, archiveErr, offline) || a.hostProbeFailures != 0 {
				t.Fatal("unavailable host must reconnect and reset consecutive failures")
			}
			if a.shouldReconnectHost(context.DeadlineExceeded, nil, false) {
				t.Fatal("new host must receive the full timeout tolerance")
			}
		})
	}
}
