package proxy

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestFR16RestartMidHoldForwardsNothing covers FR-16 and architecture §6.8. A
// restart discards every piece of in-memory state, so requests held by the old
// process are never forwarded and the fresh process starts not healthy with an
// empty held set. The check is hypha's access log: the target records every
// forwarded request, so "no forward for the held requests" is an empty record.
func TestFR16RestartMidHoldForwardsNothing(t *testing.T) {
	s := newSystem(t, systemOptions{waitBound: time.Hour})

	const n = 3
	held := s.hold(n)
	s.waitHeld(n)

	// While held, nothing has been forwarded and the target saw no request.
	if got := s.target.forwardConns(); got != 0 {
		t.Fatalf("target saw %d forwarded connections while the request was held, want 0", got)
	}
	if got := len(s.target.requests()); got != 0 {
		t.Fatalf("target received %d requests while the request was held, want 0", got)
	}

	// Restart. The old process's held connections close and its state dies.
	fresh := s.restart()

	// Every held client is released with no response bytes, because restart
	// discards the request rather than answering it.
	for _, c := range held {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		data, _ := io.ReadAll(c)
		c.Close()
		if len(data) != 0 {
			t.Fatalf("a restarted system wrote %d response bytes for a held request, want none", len(data))
		}
	}

	// The fresh process starts not healthy and with no held set (§6.8).
	if fresh.health.Healthy() {
		t.Fatal("a fresh process started healthy, want not healthy")
	}
	if got := fresh.listener.heldCount(); got != 0 {
		t.Fatalf("a fresh process started with %d held requests, want 0", got)
	}

	// Hypha's access log still shows no forward for the held requests.
	if got := fresh.target.forwardConns(); got != 0 {
		t.Fatalf("target saw %d forwarded connections after the restart, want 0 (FR-16)", got)
	}
	if got := len(fresh.target.requests()); got != 0 {
		t.Fatalf("target received %d requests after the restart, want 0 (FR-16)", got)
	}

	// The fresh process does work: once the target is ready, a new request is
	// forwarded. This proves the check above is about restart, not a dead system.
	fresh.target.setReady(true)
	newConn := fresh.send()
	data := readAll(t, newConn)
	if len(data) == 0 {
		t.Fatal("the fresh system forwarded nothing after the target became ready")
	}
}

// TestFR16ShutdownDuringWakeWritesNoResponse pins the interaction the restart
// test can only catch by timing: a wake interrupted by the process context
// (shutdown/restart) is not a wake failure, so the held client gets no response
// rather than a 500 (FR-16, §6.8).
func TestFR16ShutdownDuringWakeWritesNoResponse(t *testing.T) {
	health := NewHealth()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A wake that blocks until the context is cancelled and exec kills it.
	wake := &WakeTrigger{command: stubCommand(t, "#!/bin/sh\nsleep 30\n")}
	pl := NewPipeline(ctx, time.Hour, health,
		notReadyProber(t, health), wake,
		testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)

	time.Sleep(50 * time.Millisecond) // let the wake start
	cancel()                          // restart/shutdown

	h.wait(t)
	if resp := h.response(t, p); len(resp) != 0 {
		t.Fatalf("wrote %d bytes on shutdown during a wake, want none (FR-16): %q", len(resp), resp)
	}
}

// TestFR16StateIsInMemory makes the restart guarantee structural: a new Health
// and a new Listener share no state with any previous instance, so nothing can
// be recovered across a restart (there is no persistence seam to call).
func TestFR16StateIsInMemory(t *testing.T) {
	first := NewHealth()
	first.observe(true)
	if !first.Healthy() {
		t.Fatal("setup: health did not become healthy")
	}

	fresh := NewHealth()
	if fresh.Healthy() {
		t.Fatal("a new Health inherited a healthy belief; state is not in-memory")
	}

	l := NewListener(testConfig(), func(*Pending) {}, NewLogger(io.Discard))
	if got := l.heldCount(); got != 0 {
		t.Fatalf("a new Listener started with %d held requests, want 0", got)
	}
}
