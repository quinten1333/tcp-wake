package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tcp-wake/internal/config"
)

// This file is the integration harness the acceptance suite (T12) and the
// restart test (T10) share. It wires the real Listener, Intake, Health, Prober,
// Pipeline, WakeTrigger, Forwarder, and Logger around a controllable fake
// target, a recording wake command, and a fake client. Nothing here is a
// production type: the harness is test-only scaffolding so a requirement test
// can drive the whole request path end to end.
//
// The fake target is a raw TCP server rather than httptest.Server because the
// forward path must be observed verbatim: it reads each request with the
// production Intake so the bytes the system forwarded are the bytes recorded,
// and it writes its own response framing so chunk streaming can be controlled.

const (
	targetReadyResponse    = "HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n" + `{"status":"ok"}`
	targetNotReadyResponse = "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 25\r\n\r\n" +
		`{"error":{"message":""}}`
)

// fakeTarget is a controllable HTTP target that answers the health path from
// its ready flag and every other path with its configured response, recording
// each forwarded request. It can be started (newFakeTarget), made ready or not
// ready, given a custom forward response, and stopped (stop).
type fakeTarget struct {
	t          *testing.T
	healthPath string
	ln         net.Listener

	mu           sync.Mutex
	ready        bool
	stopped      bool
	forwardResp  string
	forward      func(c net.Conn, req string)
	got          []string
	forwardCount int
}

// newFakeTarget starts a fake target on an ephemeral loopback port. healthPath
// is the path that answers readiness rather than a forwarded response.
func newFakeTarget(t *testing.T, healthPath string) *fakeTarget {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTarget{
		t:           t,
		healthPath:  healthPath,
		ln:          ln,
		forwardResp: "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok",
	}
	go ft.acceptLoop()
	t.Cleanup(ft.stop)
	return ft
}

func (ft *fakeTarget) addr() string { return ft.ln.Addr().String() }

func (ft *fakeTarget) url() string { return "http://" + ft.addr() }

// setReady controls what the health path answers: the ready body or a 503.
func (ft *fakeTarget) setReady(ready bool) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.ready = ready
}

// setForwardResponse sets the response every forwarded request receives.
func (ft *fakeTarget) setForwardResponse(resp string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.forwardResp = resp
}

// setForwardHandler installs a custom handler for forwarded requests, for the
// streaming tests that must control the byte cadence.
func (ft *fakeTarget) setForwardHandler(fn func(c net.Conn, req string)) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.forward = fn
}

// stop closes the listener. It is idempotent and also runs on test cleanup.
func (ft *fakeTarget) stop() {
	ft.mu.Lock()
	if ft.stopped {
		ft.mu.Unlock()
		return
	}
	ft.stopped = true
	ft.mu.Unlock()
	ft.ln.Close()
}

// requests returns a copy of every request the target received on a non-health
// path.
func (ft *fakeTarget) requests() []string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]string(nil), ft.got...)
}

// forwardConns reports how many upstream connections carried a forwarded
// request. A probe connection is not counted.
func (ft *fakeTarget) forwardConns() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.forwardCount
}

func (ft *fakeTarget) acceptLoop() {
	for {
		c, err := ft.ln.Accept()
		if err != nil {
			return
		}
		go ft.serve(c)
	}
}

func (ft *fakeTarget) serve(c net.Conn) {
	defer c.Close()

	p, err := Intake(c, bufio.NewReader(c), 1<<30)
	if err != nil {
		return
	}
	req := string(p.Bytes)
	if requestPath(req) == ft.healthPath {
		ft.mu.Lock()
		ready := ft.ready
		ft.mu.Unlock()
		if ready {
			io.WriteString(c, targetReadyResponse)
		} else {
			io.WriteString(c, targetNotReadyResponse)
		}
		return
	}

	ft.mu.Lock()
	ft.got = append(ft.got, req)
	ft.forwardCount++
	resp := ft.forwardResp
	fn := ft.forward
	ft.mu.Unlock()
	if fn != nil {
		fn(c, req)
		return
	}
	io.WriteString(c, resp)
}

// requestPath extracts the path from the request line of a raw request.
func requestPath(req string) string {
	line, _, _ := strings.Cut(req, "\r\n")
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	path, _, _ := strings.Cut(fields[1], "?")
	return path
}

// system is the whole real request path under test, wired around a fake target.
// It can be restarted by cancelling its context and building a fresh instance,
// which is how the restart semantics (FR-16) are exercised.
type system struct {
	t      *testing.T
	target *fakeTarget
	cfg    *config.Config

	health   *Health
	prober   *Prober
	pipeline *Pipeline
	listener *Listener
	log      *syncBuffer
	wakeRec  string
	addr     string

	cancel context.CancelFunc
	done   chan error
}

// systemOptions tweak the harness for one test.
type systemOptions struct {
	probeInterval time.Duration
	waitBound     time.Duration
	wakeCommand   string
	heldBodyCap   config.ByteSize
}

// newSystem builds and starts the whole request path against a fake target that
// starts not ready, with a wake command that records each execution and exits
// zero. Callers flip readiness with s.target.setReady(true).
func newSystem(t *testing.T, opts systemOptions) *system {
	t.Helper()

	target := newFakeTarget(t, "/health")

	wakeCmd := opts.wakeCommand
	wakeRec := ""
	if wakeCmd == "" {
		wakeCmd, wakeRec = recordingCommand(t)
	}

	probeInterval := opts.probeInterval
	if probeInterval == 0 {
		probeInterval = 5 * time.Millisecond
	}
	waitBound := opts.waitBound
	if waitBound == 0 {
		waitBound = 2 * time.Second
	}
	heldCap := opts.heldBodyCap
	if heldCap == 0 {
		heldCap = 1 << 20
	}

	cfg := &config.Config{
		ListenAddress: "127.0.0.1:0",
		TargetAddress: target.url(),
		HealthPath:    "/health",
		ProbeInterval: probeInterval,
		ProbeTimeout:  100 * time.Millisecond,
		WaitBound:     waitBound,
		WakeCommand:   wakeCmd,
		HeldBodyCap:   heldCap,
	}
	return startSystem(t, cfg, target, wakeRec)
}

// restart tears the system down and returns a fresh one with the same
// configuration and target, matching a process restart: all in-memory state is
// gone and the new process starts not healthy.
func (s *system) restart() *system {
	s.t.Helper()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		s.t.Fatal("system did not stop on restart")
	}
	return startSystem(s.t, s.cfg, s.target, s.wakeRec)
}

// startSystem wires one instance of the request path. It is split out so
// restart can build a second instance with the same config and target.
func startSystem(t *testing.T, cfg *config.Config, target *fakeTarget, wakeRec string) *system {
	t.Helper()
	log := &syncBuffer{}
	health := NewHealth()
	prober := NewProber(cfg, health)
	forwarder, err := NewForwarder(cfg.TargetAddress)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pipeline := NewPipeline(ctx, cfg.WaitBound, health, prober, NewWakeTrigger(cfg), forwarder, NewLogger(log))
	listener := NewListener(cfg, pipeline.Handle, NewLogger(log))

	addrCh := make(chan string, 1)
	listener.listen = func(network, address string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		addrCh <- ln.Addr().String()
		return ln, nil
	}
	done := make(chan error, 1)
	go func() { done <- listener.Serve(ctx) }()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("system listener did not start")
	}

	s := &system{
		t: t, target: target, cfg: cfg,
		health: health, prober: prober, pipeline: pipeline, listener: listener,
		log: log, wakeRec: wakeRec, addr: addr,
		cancel: cancel, done: done,
	}
	t.Cleanup(s.cancel)
	t.Cleanup(prober.Close)
	return s
}

// hold opens n client connections, sends a complete GET on each, and returns
// them. The request is accepted but not answered while the target is not ready.
func (s *system) hold(n int) []net.Conn {
	s.t.Helper()
	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, s.send())
	}
	return conns
}

// send opens one client connection and writes a complete GET request, leaving
// the connection open to hold or receive the response.
func (s *system) send() net.Conn {
	s.t.Helper()
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		s.t.Fatalf("dial system: %v", err)
	}
	if _, err := io.WriteString(conn, "GET /v1/models HTTP/1.1\r\nHost: hypha\r\n\r\n"); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
	return conn
}

// sendRaw opens one client connection and writes raw, for tests that need a
// specific request shape (a body, a bad framing, or a sentinel token).
func (s *system) sendRaw(raw string) net.Conn {
	s.t.Helper()
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		s.t.Fatalf("dial system: %v", err)
	}
	if _, err := io.WriteString(conn, raw); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
	return conn
}

// waitHeld blocks until the listener reports exactly n held requests.
func (s *system) waitHeld(n int) {
	s.t.Helper()
	waitFor(s.t, fmt.Sprintf("%d requests held", n), func() bool {
		return s.listener.heldCount() == n
	})
}

// wakeExecutions counts the wake-command executions recorded by the stub. The
// record file is written by the child before cmd.Run returns, so a caller that
// needs to be sure the write landed should wait on the log line instead
// (wakeLogLines).
func (s *system) wakeExecutions() int {
	if s.wakeRec == "" {
		return 0
	}
	return countLines(s.wakeRec)
}

// wakeLogLines counts the wake lines in the captured log, the counting
// artefact for FR-3, FR-9, and FR-12.
func (s *system) wakeLogLines() int { return wakeLineCount(s.log.String()) }

// readAll closes the client connection and returns everything the system wrote
// on it. It is safe to call once per held connection after the response or
// after the client is done.
func readAll(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading client response: %v", err)
	}
	conn.Close()
	return data
}

// readResponseHead reads and parses one response's status and Content-Length
// framing from a streaming client, for the chunk-cadence tests.
func readResponseHead(t *testing.T, r *bufio.Reader) (status int) {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("reading response status line: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("bad status line %q", line)
	}
	status, err = strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("bad status line %q: %v", line, err)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading response head: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			return status
		}
	}
}

// TestHarnessSelfCheck is T11's "runs green against a stub implementation"
// check: it drives the whole wired path once and asserts each harness block did
// its job. If the target, the wake record, the listener, or the log wiring were
// broken, this fails before the requirement tests do, which keeps a harness bug
// from masquerading as a requirement failure.
func TestHarnessSelfCheck(t *testing.T) {
	s := newSystem(t, systemOptions{waitBound: time.Second})

	// A request while the target is not ready is held, wakes once, and sends
	// no bytes.
	conn := s.send()
	s.waitHeld(1)
	if got := len(s.target.requests()); got != 0 {
		t.Fatalf("harness: target saw %d forwards while not ready, want 0", got)
	}
	waitFor(t, "wake log line", func() bool { return s.wakeLogLines() == 1 })
	if got := s.wakeExecutions(); got != 1 {
		t.Fatalf("harness: wake command ran %d time(s), want 1", got)
	}

	// When the target becomes ready it forwards and relays the response.
	s.target.setReady(true)
	data := readAll(t, conn)
	if len(data) == 0 {
		t.Fatal("harness: a released request received no response")
	}
	if got := s.target.forwardConns(); got != 1 {
		t.Fatalf("harness: target saw %d forwards after ready, want 1", got)
	}
	if got := len(s.target.requests()); got != 1 {
		t.Fatalf("harness: target recorded %d requests after ready, want 1", got)
	}
}
