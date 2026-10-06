package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"strings"
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

// newPipePendingBytes returns a Pending whose retained bytes are raw and whose
// client side is the other end of a net.Pipe, so a test can read the response.
func newPipePendingBytes(t *testing.T, raw string) (*Pending, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	p := newPending(server, bufio.NewReader(server), []byte(raw))
	return p, client
}

// startListener runs a Listener bound to an ephemeral loopback port, returning
// it and the address a client can dial. The listener is cancelled on cleanup.
// Its logger discards, since only the 413 path logs from the listener; tests
// that assert that line use startListenerLogged.
func startListener(t *testing.T, cfg *config.Config, h Handler) (*Listener, string) {
	t.Helper()
	return startListenerLogged(t, cfg, h, NewLogger(io.Discard))
}

// startListenerLogged is startListener with a chosen logger, so a test can
// assert the listener's own error line (the 413).
func startListenerLogged(t *testing.T, cfg *config.Config, h Handler, logger *Logger) (*Listener, string) {
	t.Helper()
	l := NewListener(cfg, h, logger)
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

// testConfig returns a config with valid positive cadences so a test that
// starts the real Prober loop cannot hit time.NewTicker's non-positive panic.
func testConfig() *config.Config {
	return &config.Config{
		ListenAddress: "127.0.0.1:0",
		ProbeInterval: time.Second,
		ProbeTimeout:  time.Second,
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

// scanNonTestSources calls check for every non-test .go file in the package, so
// the several structural guards share one directory walk and one "checked at
// least one file" assertion.
func scanNonTestSources(t *testing.T, check func(name string, data []byte)) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		check(name, data)
	}
	if checked == 0 {
		t.Fatal("source inspection checked no files")
	}
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

func (b *blockOnDiscard) handle(p *Pending) {
	b.mu.Lock()
	b.got = append(b.got, p)
	b.mu.Unlock()
	<-p.Discarded()
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

// syncBuffer is a bytes.Buffer safe for the goroutine that logs and the test
// goroutine that reads it, so a -race run is not tripped by the test's own
// observation of a concurrently written log.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
