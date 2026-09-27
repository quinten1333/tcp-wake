package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newPipePending returns a Pending whose client side is the other end of a
// net.Pipe, so a test can read the response the pipeline writes.
func newPipePending(t *testing.T) (*Pending, net.Conn) {
	t.Helper()
	return newPipePendingBytes(t, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
}

// wakeStub is a wake trigger for a command that exits cleanly, so the FR-8
// tests exercise the wait path rather than the FR-17 failure path.
func wakeStub(t *testing.T) *WakeTrigger {
	t.Helper()
	cfg := testConfig()
	cfg.WakeCommand = stubCommand(t, "#!/bin/sh\nexit 0\n")
	return NewWakeTrigger(cfg)
}

// handled tracks one Handle call running against a client that reads its
// response. net.Pipe is synchronous, so the reader must run concurrently or a
// response write would block.
type handled struct {
	start time.Time
	done  chan struct{}
	data  chan []byte
}

func startHandle(pl *Pipeline, p *Pending, client net.Conn) *handled {
	h := &handled{start: time.Now(), done: make(chan struct{}), data: make(chan []byte, 1)}
	go func() {
		data, _ := io.ReadAll(client)
		h.data <- data
	}()
	go func() {
		pl.Handle(p)
		close(h.done)
	}()
	return h
}

// wait blocks until Handle returns, failing the test if it does not, and
// returns how long it took.
func (h *handled) wait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle did not return")
	}
	return time.Since(h.start)
}

// response closes the client connection so the reader sees EOF and returns the
// bytes the pipeline wrote (empty when it wrote nothing).
func (h *handled) response(t *testing.T, p *Pending) []byte {
	t.Helper()
	p.Conn.Close()
	select {
	case data := <-h.data:
		return data
	case <-time.After(time.Second):
		t.Fatal("client reader did not finish")
		return nil
	}
}

// notReadyProber is a prober whose probe never reports ready, so the belief
// stays not healthy for the whole wait.
func notReadyProber(t *testing.T, health *Health) *Prober {
	t.Helper()
	pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
	t.Cleanup(pr.Close)
	return pr
}

// TestFR8WaitBoundExpiryGets504NamingBound covers FR-8: a request whose target
// never becomes healthy leaves the wait at the bound with a 504 naming it.
func TestFR8WaitBoundExpiryGets504NamingBound(t *testing.T) {
	health := NewHealth()
	pl := NewPipeline(context.Background(), 150*time.Millisecond, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)
	elapsed := h.wait(t)
	resp := string(h.response(t, p))

	if elapsed < 150*time.Millisecond {
		t.Fatalf("returned after %v, before the bound", elapsed)
	}
	status, detail := parseErrorResponse(t, []byte(resp))
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", status)
	}
	if detail.Component != componentWaitBound {
		t.Fatalf("504 body does not name wait_bound: %+v", detail)
	}
	if detail.Limit != "150ms" {
		t.Fatalf("504 body does not carry the bound: %+v", detail)
	}
}

// TestFR8TimerMeasuredFromArrival covers the "measured from the request's
// arrival" half of FR-8: a request that arrived before Handle started gets the
// remaining time, not a fresh full bound.
func TestFR8TimerMeasuredFromArrival(t *testing.T) {
	health := NewHealth()
	pl := NewPipeline(context.Background(), time.Second, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p, client := newPipePending(t)
	p.Arrival = time.Now().Add(-900 * time.Millisecond) // ~100ms left
	h := startHandle(pl, p, client)
	elapsed := h.wait(t)
	resp := string(h.response(t, p))

	if elapsed >= 600*time.Millisecond {
		t.Fatalf("waited %v; the bound was not measured from arrival", elapsed)
	}
	if _, detail := parseErrorResponse(t, []byte(resp)); detail.Component != componentWaitBound {
		t.Fatalf("no wait_bound 504 was written: %+v", detail)
	}
}

// TestFR8SecondWakeDoesNotExtend covers "never restarted by a wake attempt": a
// second execution while the request is held must not push the deadline out.
func TestFR8SecondWakeDoesNotExtend(t *testing.T) {
	health := NewHealth()
	pl := NewPipeline(context.Background(), 500*time.Millisecond, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)

	time.Sleep(200 * time.Millisecond)
	pl.wake.Run(context.Background()) // a second wake attempt

	elapsed := h.wait(t)
	h.response(t, p)

	if elapsed >= 650*time.Millisecond {
		t.Fatalf("waited %v; the second wake attempt extended the bound", elapsed)
	}
	if elapsed < 450*time.Millisecond {
		t.Fatalf("waited only %v; the bound fired early", elapsed)
	}
}

// TestFR8ClientDisconnectGetsNoResponse covers the hold contract's other end: a
// client that leaves while held gets no response bytes (FR-7), so a disconnect
// must not be mistaken for a wait-bound expiry.
func TestFR8ClientDisconnectGetsNoResponse(t *testing.T) {
	health := NewHealth()
	pl := NewPipeline(context.Background(), 5*time.Second, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)

	time.Sleep(50 * time.Millisecond)
	p.Discard()

	elapsed := h.wait(t)
	resp := h.response(t, p)
	if elapsed >= time.Second {
		t.Fatalf("waited %v after the client disconnected", elapsed)
	}
	if len(resp) != 0 {
		t.Fatalf("wrote %d bytes to a client that left:\n%s", len(resp), resp)
	}
}

// TestFR8IndependentTimers covers the RS-5 failure case: each request has its
// own timer, and one expiry does not affect another request's wait.
func TestFR8IndependentTimers(t *testing.T) {
	health := NewHealth()
	pl := NewPipeline(context.Background(), 400*time.Millisecond, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p1, c1 := newPipePending(t)
	p1.Arrival = time.Now().Add(-350 * time.Millisecond) // ~50ms left
	p2, c2 := newPipePending(t)                          // full 400ms

	h1 := startHandle(pl, p1, c1)
	h2 := startHandle(pl, p2, c2)

	first := h1.wait(t)
	select {
	case <-h2.done:
		t.Fatal("the later request finished before the earlier one")
	default:
	}
	second := h2.wait(t)

	h1.response(t, p1)
	h2.response(t, p2)

	if first >= second {
		t.Fatalf("earlier request finished at %v, later at %v", first, second)
	}
}

// TestFR8HealthyBeforeBoundForwards covers the "does not become healthy within
// the bound" condition: a target that turns healthy before the bound forwards
// the request rather than answering with a 504.
func TestFR8HealthyBeforeBoundForwards(t *testing.T) {
	const resp = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
	addr := rawTarget(t, func(c net.Conn) {
		readRequest(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		io.WriteString(c, resp)
	})

	health := NewHealth()
	prober := stubProber(health, 5*time.Millisecond, func(context.Context) bool { return true })
	t.Cleanup(prober.Close)
	pl := NewPipeline(context.Background(), 2*time.Second, health,
		prober, wakeStub(t), testForwarder(t, "http://"+addr), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)
	elapsed := h.wait(t)
	got := string(h.response(t, p))

	if elapsed >= time.Second {
		t.Fatalf("waited %v despite becoming healthy", elapsed)
	}
	if got != resp {
		t.Fatalf("client received %q, want the target's %q", got, resp)
	}
}

// TestFR8WakeFailureDoesNotWait covers the ordering in Handle: a failed wake
// still answers immediately (FR-17) even with a long wait bound.
func TestFR8WakeFailureDoesNotWait(t *testing.T) {
	health := NewHealth()
	cfg := testConfig()
	cfg.WakeCommand = failingCommand(t, 3)
	pl := NewPipeline(context.Background(), 10*time.Second, health,
		notReadyProber(t, health), NewWakeTrigger(cfg), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)
	elapsed := h.wait(t)
	resp := string(h.response(t, p))

	if elapsed >= time.Second {
		t.Fatalf("waited %v before answering a failed wake", elapsed)
	}
	if !strings.HasPrefix(resp, "HTTP/1.1 500") {
		t.Fatalf("response is not a 500:\n%s", resp)
	}
	if !strings.Contains(resp, `"component":"wake_command"`) {
		t.Fatalf("500 body does not name the wake command:\n%s", resp)
	}
}
