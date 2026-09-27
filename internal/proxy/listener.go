package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"tcp-wake/internal/config"
)

// Handler receives a request once Intake has retained it. It must not write to
// the client while the request is held (FR-2, FR-6); it returns when the
// request has been released, answered, or abandoned.
type Handler func(*Pending) error

// Listener accepts client connections on the configured address and hands each
// retained request to the handler. One goroutine serves one connection, so a
// held request is a blocked goroutine and no deadline is ever set (ADR-0001,
// NFR-4).
type Listener struct {
	cfg    *config.Config
	held   *HeldSet
	handle Handler

	// listen is net.Listen in production and is injectable so tests can bind
	// a loopback port and learn its address.
	listen func(network, address string) (net.Listener, error)
}

// New builds a Listener for cfg. handle is invoked for every request that
// Intake retains.
func New(cfg *config.Config, handle Handler) *Listener {
	return &Listener{
		cfg:    cfg,
		held:   NewHeldSet(),
		handle: handle,
		listen: net.Listen,
	}
}

// Held exposes the held set so the pipeline and tests can observe membership.
func (l *Listener) Held() *HeldSet {
	return l.held
}

// Serve accepts connections until ctx is cancelled or the listener fails. On
// return every held connection has been closed.
func (l *Listener) Serve(ctx context.Context) error {
	ln, err := l.listen("tcp", l.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("proxy: listen on %s: %w", l.cfg.ListenAddress, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			l.held.CloseAll()
			wg.Wait()
			return fmt.Errorf("proxy: accept: %w", err)
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			l.serveConn(c)
		}(conn)
	}

	l.held.CloseAll()
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
		conn.Close()
		return
	}

	l.held.Add(p)
	go l.watchClient(p)
	defer func() {
		p.Discard()
		l.held.Remove(p)
		conn.Close()
	}()

	_ = l.handle(p)
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
			l.held.Remove(p)
			return
		}
	}
}
