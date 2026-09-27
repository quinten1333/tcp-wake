package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"tcp-wake/internal/config"
)

// Handler receives a request once Intake has retained it. It must not write to
// the client while the request is held (FR-2, FR-6); it returns when the
// request has been released, answered, or abandoned. It returns nothing: the
// pipeline writes its own responses and logs its own errors, so a returned
// error would be dead surface.
type Handler func(*Pending)

// Listener accepts client connections on the configured address and hands each
// retained request to the handler. One goroutine serves one connection, so a
// held request is a blocked goroutine and no deadline is ever set (ADR-0001,
// NFR-4).
type Listener struct {
	cfg    *config.Config
	held   *heldSet
	handle Handler
	logger *Logger

	// listen is net.Listen in production and is injectable so tests can bind
	// a loopback port and learn its address.
	listen func(network, address string) (net.Listener, error)
}

// NewListener builds a Listener for cfg. handle is invoked for every request
// that Intake retains. logger records a body-cap rejection, the one error
// produced here rather than in the pipeline.
func NewListener(cfg *config.Config, handle Handler, logger *Logger) *Listener {
	return &Listener{
		cfg:    cfg,
		held:   newHeldSet(),
		handle: handle,
		logger: logger,
		listen: net.Listen,
	}
}

// heldCount reports how many requests are currently held. It is a read-only
// observation seam for tests.
func (l *Listener) heldCount() int {
	return l.held.count()
}

// Serve accepts connections until ctx is cancelled or the listener fails. On
// return every held connection has been closed.
func (l *Listener) Serve(ctx context.Context) error {
	ln, err := l.listen("tcp", l.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("proxy: listen on %s: %w", l.cfg.ListenAddress, err)
	}
	defer ln.Close()

	// Close the listener when the context is cancelled, and stop this watcher
	// when Serve returns for any other reason so the goroutine cannot outlive
	// the call.
	stopWatching := make(chan struct{})
	defer close(stopWatching)
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-stopWatching:
		}
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			l.held.closeAll()
			wg.Wait()
			return fmt.Errorf("proxy: accept: %w", err)
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			l.serveConn(c)
		}(conn)
	}

	l.held.closeAll()
	wg.Wait()
	return nil
}

// serveConn retains one request and hands it to the handler. The close watcher
// runs alongside so a client that disconnects while held leaves the set (FR-7);
// the deferred cleanup makes the removal idempotent once the request is
// released.
func (l *Listener) serveConn(conn net.Conn) {
	r := bufio.NewReader(conn)
	p, err := Intake(conn, r, l.cfg.HeldBodyCap)
	if err != nil {
		// The 413 is the only error produced outside the pipeline; Intake has
		// already written it, so log the same detail (ADR-0010). A framing
		// error produces no response and is deliberately not logged.
		if errors.Is(err, ErrBodyTooLarge) {
			l.logger.Error(http.StatusRequestEntityTooLarge, heldBodyCapDetail(l.cfg.HeldBodyCap))
		}
		conn.Close()
		return
	}

	l.held.add(p)
	go l.watchClient(p)
	defer func() {
		p.Discard()
		l.held.remove(p)
		conn.Close()
	}()

	l.handle(p)
}

// watchClient blocks in Read until the client closes its connection or sends
// more bytes. A closed connection discards the request (FR-7); extra bytes are
// ignored because a pipelined request is out of scope and cannot be forwarded.
// No deadline is set: the blocked Read is how the close is observed (NFR-4).
func (l *Listener) watchClient(p *Pending) {
	buf := make([]byte, 512)
	for {
		if _, err := p.reader.Read(buf); err != nil {
			p.Discard()
			l.held.remove(p)
			return
		}
	}
}
