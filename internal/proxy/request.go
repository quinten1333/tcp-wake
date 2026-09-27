// Package proxy implements the tcp-wake request pipeline. The blocks here are
// the Listener (accept a client connection), Intake (retain one request as the
// exact wire bytes the client sent), the held set (the in-memory set of
// requests currently held), the Health belief and its Prober (the target's
// readiness, written only by a probe), the WakeTrigger (one command execution
// per request received while not healthy), the Logger (one line per execution),
// and the Pipeline that orders them along the request path.
//
// Bytes are never parsed into a request object: the header/body boundary and
// the two framing fields (Content-Length, Transfer-Encoding) are inspected so
// the end of the request can be detected, but every byte retained and later
// forwarded is the byte the client sent (ADR-0004). No read or write deadline
// is ever set on a connection (NFR-4); a held request is a blocked goroutine,
// which is why the client-close watch uses a blocking Read rather than a
// deadline (ADR-0001).
package proxy

import (
	"bufio"
	"net"
	"sync"
	"time"
)

// Pending is a request that has been accepted and retained, awaiting a
// response. Its fields are read by the forwarding block; the reader field is
// owned by the close watcher once Intake has returned.
type Pending struct {
	// Conn is the client connection the request arrived on.
	Conn net.Conn
	// Bytes is the exact request as received, headers and body included.
	Bytes []byte
	// Arrival is when the request was accepted, the origin of the wait bound
	// measured from arrival (FR-8).
	Arrival time.Time

	reader      *bufio.Reader
	discardOnce sync.Once
	discard     chan struct{}
}

func newPending(conn net.Conn, r *bufio.Reader, raw []byte) *Pending {
	return &Pending{
		Conn:    conn,
		Bytes:   raw,
		Arrival: time.Now(),
		reader:  r,
		discard: make(chan struct{}),
	}
}

// Discarded returns a channel that is closed when the request has been
// abandoned, either because the client closed its connection (FR-7) or because
// the process is shutting down.
func (p *Pending) Discarded() <-chan struct{} {
	return p.discard
}

// Discard marks the request as abandoned. It is idempotent so the close
// watcher, the release path, and shutdown can all call it without a double
// close.
func (p *Pending) Discard() {
	p.discardOnce.Do(func() {
		close(p.discard)
	})
}
