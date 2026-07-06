// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The dead-man heartbeat is a fixed cadence, decoupled from drill schedules:
// startup ping plus ticker beats — a weekly-only config must not read as a
// dead daemon to a monitor with a sane grace period.
func TestRunHeartbeatCadence(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = old })

	var pings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pings.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runHeartbeat(ctx, srv.URL, newLogger(io.Discard)) }()

	deadline := time.Now().Add(3 * time.Second)
	for pings.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if got := pings.Load(); got < 3 {
		t.Fatalf("pings = %d, want >=3 (startup + ticker beats)", got)
	}
}
