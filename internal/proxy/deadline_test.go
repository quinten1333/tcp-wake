package proxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deadlineSpy records any attempt to set a deadline on a connection, so NFR-4
// can be checked by execution rather than only by reading the code.
type deadlineSpy struct {
	net.Conn
	read  int32
	write int32
	any   int32
}

func (d *deadlineSpy) SetReadDeadline(time.Time) error {
	atomic.AddInt32(&d.read, 1)
	return nil
}

func (d *deadlineSpy) SetWriteDeadline(time.Time) error {
	atomic.AddInt32(&d.write, 1)
	return nil
}

func (d *deadlineSpy) SetDeadline(time.Time) error {
	atomic.AddInt32(&d.any, 1)
	return nil
}

func (d *deadlineSpy) calls() int {
	return int(atomic.LoadInt32(&d.read) + atomic.LoadInt32(&d.write) + atomic.LoadInt32(&d.any))
}

type spyListener struct {
	net.Listener
	mu    sync.Mutex
	spies []*deadlineSpy
}

func (s *spyListener) Accept() (net.Conn, error) {
	c, err := s.Listener.Accept()
	if err != nil {
		return c, err
	}
	sp := &deadlineSpy{Conn: c}
	s.mu.Lock()
	s.spies = append(s.spies, sp)
	s.mu.Unlock()
	return sp, nil
}

func (s *spyListener) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, sp := range s.spies {
		total += sp.calls()
	}
	return total
}

// TestNFR4ListenerSetsNoDeadline holds a request through the real listener and
// asserts no read or write deadline was ever set on the client connection.
func TestNFR4ListenerSetsNoDeadline(t *testing.T) {
	l := NewListener(testConfig(), func(p *Pending) {
		<-p.Discarded()
	}, NewLogger(io.Discard))

	addrCh := make(chan string, 1)
	var sl *spyListener
	l.listen = func(_, _ string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		sl = &spyListener{Listener: ln}
		addrCh <- ln.Addr().String()
		return sl, nil
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

	addr := <-addrCh
	conn := get(t, addr)
	waitFor(t, "request held", func() bool { return l.heldCount() == 1 })
	conn.Close()
	waitFor(t, "held set empty", func() bool { return l.heldCount() == 0 })

	if got := sl.calls(); got != 0 {
		t.Fatalf("deadline set %d time(s) on a held connection, want 0 (NFR-4)", got)
	}
}

// TestNFR4NoDeadlineCallsInSource is the inspection NFR-4 asks for: the
// connection-handling code contains no deadline call at all. Test files are
// excluded because the spy above names the methods to prove they are absent
// from the implementation.
func TestNFR4NoDeadlineCallsInSource(t *testing.T) {
	banned := []string{"SetReadDeadline", "SetWriteDeadline", "SetDeadline"}
	scanNonTestSources(t, func(name string, data []byte) {
		for _, b := range banned {
			if bytes.Contains(data, []byte(b)) {
				t.Errorf("%s sets a %s, which NFR-4 forbids", name, b)
			}
		}
	})
}
