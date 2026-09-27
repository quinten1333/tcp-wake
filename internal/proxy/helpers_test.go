package proxy

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"tcp-wake/internal/config"
)

const testCap = config.ByteSize(1 << 20)

// intakeOnce drives Intake over a net.Pipe with a client that writes req and
// asserts the request is retained without error.
func intakeOnce(t *testing.T, req string, limit config.ByteSize) *Pending {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.WriteString(client, req)
	}()

	p, err := Intake(server, bufio.NewReader(server), limit)
	if err != nil {
		t.Fatalf("Intake(%q) returned error: %v", req, err)
	}
	<-done
	return p
}

// startListener runs a Listener bound to an ephemeral loopback port, returning
// it and the address a client can dial. The listener is cancelled on cleanup.
func startListener(t *testing.T, cfg *config.Config, h Handler) (*Listener, string) {
	t.Helper()
	l := New(cfg, h)
	addrCh := make(chan string, 1)
	l.listen = func(network, address string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		addrCh <- ln.Addr().String()
		return ln, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not return after cancellation")
		}
	})

	select {
	case addr := <-addrCh:
		return l, addr
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not start")
		return nil, ""
	}
}

func testConfig() *config.Config {
	return &config.Config{
		ListenAddress: "127.0.0.1:0",
		HeldBodyCap:   testCap,
	}
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// get sends a minimal complete request and leaves the connection open.
func get(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return conn
}

// blockOnDiscard is a Handler that holds each request until its client goes
// away, recording the Pending it saw.
type blockOnDiscard struct {
	mu  sync.Mutex
	got []*Pending
}

func (b *blockOnDiscard) handle(p *Pending) error {
	b.mu.Lock()
	b.got = append(b.got, p)
	b.mu.Unlock()
	<-p.Discarded()
	return nil
}

func (b *blockOnDiscard) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.got)
}

func (b *blockOnDiscard) first() *Pending {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.got) == 0 {
		return nil
	}
	return b.got[0]
}
