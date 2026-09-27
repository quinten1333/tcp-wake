package proxy

import (
	"net"
	"testing"
	"time"
)

// TestIF1ListenerHandsOffRequest covers the listener accepting a request over
// HTTP and handing the retained bytes to the pipeline over the same connection.
func TestIF1ListenerHandsOffRequest(t *testing.T) {
	got := make(chan string, 1)
	_, addr := startListener(t, testConfig(), func(p *Pending) error {
		got <- string(p.Bytes)
		return nil
	})

	conn := get(t, addr)
	defer conn.Close()

	req := "GET / HTTP/1.1\r\nHost: x\r\n\r\n"
	select {
	case b := <-got:
		if b != req {
			t.Fatalf("handed off %q, want %q", b, req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
}

// TestFR6NoBytesSentWhileHeld asserts the client receives zero bytes while a
// request is held (FR-6).
func TestFR6NoBytesSentWhileHeld(t *testing.T) {
	release := make(chan struct{})
	l, addr := startListener(t, testConfig(), func(p *Pending) error {
		<-release
		return nil
	})

	conn := get(t, addr)
	defer conn.Close()
	waitFor(t, "request held", func() bool { return l.Held().Len() == 1 })

	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("received %d bytes while held, want none", n)
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("read returned %v, want a timeout with no bytes", err)
	}
	close(release)
}

// TestFR7ClientCloseDiscardsHeldRequest asserts a client that closes while held
// is removed from the held set and its handler is released (FR-7).
func TestFR7ClientCloseDiscardsHeldRequest(t *testing.T) {
	h := &blockOnDiscard{}
	l, addr := startListener(t, testConfig(), h.handle)

	conn := get(t, addr)
	waitFor(t, "request held", func() bool { return l.Held().Len() == 1 })
	waitFor(t, "handler invoked", func() bool { return h.count() == 1 })
	p := h.first()

	conn.Close()

	waitFor(t, "held set empty after client close", func() bool { return l.Held().Len() == 0 })
	select {
	case <-p.Discarded():
	case <-time.After(2 * time.Second):
		t.Fatal("held request was not discarded when the client closed")
	}
}

// TestHoldAtLeastEightConcurrent is the floor NFR-5 tests: eight concurrent
// requests during one window are all retained without discarding any. The full
// end-to-end check lands with the forwarder (T7).
func TestHoldAtLeastEightConcurrent(t *testing.T) {
	h := &blockOnDiscard{}
	l, addr := startListener(t, testConfig(), h.handle)

	const n = 8
	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, get(t, addr))
	}
	waitFor(t, "eight requests held", func() bool { return l.Held().Len() == n })
	waitFor(t, "eight handlers invoked", func() bool { return h.count() == n })

	for _, c := range conns {
		c.Close()
	}
	waitFor(t, "held set empty", func() bool { return l.Held().Len() == 0 })
}
