package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rawTarget starts a loopback TCP server running serve on each accepted
// connection and returns its host:port. It is a raw target so tests control the
// exact response bytes.
func rawTarget(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				serve(c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// testForwarder builds a Forwarder for a live or deliberately dead target.
func testForwarder(t *testing.T, targetAddress string) *Forwarder {
	t.Helper()
	f, err := NewForwarder(targetAddress)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// closedAddr returns a loopback address nothing listens on, for the transport
// failure tests.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// runForward drives Forward while a goroutine reads the client side (net.Pipe is
// synchronous, so a response write would otherwise block), then returns the
// relayed bytes and Forward's error.
func runForward(t *testing.T, f *Forwarder, p *Pending, client net.Conn) ([]byte, error) {
	t.Helper()
	dataCh := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(client)
		dataCh <- data
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- f.Forward(p) }()

	var ferr error
	select {
	case ferr = <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Forward did not return")
	}
	p.Conn.Close()

	select {
	case data := <-dataCh:
		return data, ferr
	case <-time.After(time.Second):
		t.Fatal("client reader did not finish")
		return nil, nil
	}
}

// readRequest reads exactly want from c, so a test target knows when the
// forwarded request is complete before it answers.
func readRequest(c net.Conn, want string) {
	buf := make([]byte, len(want))
	io.ReadFull(c, buf)
}

// TestIF2ForwardAndRelayRequestResponse covers IF-2, FR-13, and FR-14: the
// target receives the client's request bytes verbatim and the client receives
// the target's response bytes verbatim, odd casing and order included.
func TestIF2ForwardAndRelayRequestResponse(t *testing.T) {
	const req = "POST /v1/embeddings HTTP/1.1\r\nhOsT: hypha\r\nX-Odd-Case: Keep\r\nContent-Length: 5\r\n\r\nhello"
	const resp = "HTTP/1.1 200 OK\r\nx-miXed: Yes\r\nContent-Length: 11\r\n\r\nhello world"

	gotReq := make(chan string, 1)
	addr := rawTarget(t, func(c net.Conn) {
		buf := make([]byte, len(req))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		gotReq <- string(buf)
		io.WriteString(c, resp)
	})

	p, client := newPipePendingBytes(t, req)
	data, err := runForward(t, testForwarder(t, "http://"+addr), p, client)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}

	select {
	case got := <-gotReq:
		if got != req {
			t.Fatalf("target received %q, want %q", got, req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("target never received the request")
	}
	if string(data) != resp {
		t.Fatalf("client received %q, want %q", data, resp)
	}
}

// TestForwardRelaysUntilClose covers a response with no Content-Length and no
// chunked framing: the body is relayed until the target closes.
func TestForwardRelaysUntilClose(t *testing.T) {
	const resp = "HTTP/1.1 200 OK\r\nX: y\r\n\r\nbody-till-close"
	addr := rawTarget(t, func(c net.Conn) {
		readRequest(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		io.WriteString(c, resp)
	})

	p, client := newPipePending(t)
	data, err := runForward(t, testForwarder(t, "http://"+addr), p, client)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if string(data) != resp {
		t.Fatalf("client received %q, want %q", data, resp)
	}
}

// TestForwardSwallowsPostHeaderFailureIsOneResponse covers NFR-6: once the
// response head is on the wire, a target that dies mid-body closes the
// connection rather than receiving a second (502) response, and Forward reports
// no error.
func TestForwardSwallowsPostHeaderFailureIsOneResponse(t *testing.T) {
	const head = "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n"
	addr := rawTarget(t, func(c net.Conn) {
		readRequest(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		io.WriteString(c, head)
		io.WriteString(c, "short") // fewer than the promised 100 bytes
	})

	p, client := newPipePending(t)
	data, err := runForward(t, testForwarder(t, "http://"+addr), p, client)
	if err != nil {
		t.Fatalf("Forward returned an error after the head was written: %v", err)
	}
	if string(data) != head+"short" {
		t.Fatalf("client received %q, want the partial response and no 502", data)
	}
}

// TestFR15StreamsChunksUnbuffered covers FR-15: a chunked response is relayed one
// chunk at a time. The target waits for the client to acknowledge each chunk
// before sending the next, so an implementation that buffered the body would
// deadlock rather than pass.
func TestFR15StreamsChunksUnbuffered(t *testing.T) {
	const n = 100
	acked := make([]chan struct{}, n)
	for i := range acked {
		acked[i] = make(chan struct{})
	}

	addr := rawTarget(t, func(c net.Conn) {
		readRequest(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		io.WriteString(c, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
		for i := 0; i < n; i++ {
			body := fmt.Sprintf("chunk-%03d", i)
			fmt.Fprintf(c, "%x\r\n%s\r\n", len(body), body)
			<-acked[i]
		}
		io.WriteString(c, "0\r\n\r\n")
	})

	p, client := newPipePending(t)
	errCh := make(chan error, 1)
	go func() { errCh <- testForwarder(t, "http://"+addr).Forward(p) }()

	// Read each chunk and acknowledge it before the target sends the next.
	go func() {
		r := bufio.NewReader(client)
		// Consume the response head before the chunks.
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" || line == "\n" {
				break
			}
		}
		for i := 0; i < n; i++ {
			sizeLine, err := r.ReadString('\n')
			if err != nil {
				return
			}
			sz, err := strconv.ParseInt(strings.TrimSpace(sizeLine), 16, 64)
			if err != nil {
				t.Errorf("bad chunk size %q: %v", sizeLine, err)
				return
			}
			body := make([]byte, sz)
			if _, err := io.ReadFull(r, body); err != nil {
				return
			}
			if _, err := r.ReadString('\n'); err != nil {
				return
			}
			want := fmt.Sprintf("chunk-%03d", i)
			if string(body) != want {
				t.Errorf("chunk %d = %q, want %q", i, body, want)
				return
			}
			close(acked[i])
		}
		// Drain the terminating chunk and its trailer so the relay is not left
		// blocking on a write no one reads.
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil || line == "\r\n" || line == "\n" {
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Forward: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the streamed response was batched or never finished")
	}
	p.Conn.Close()
}

// TestFR1HealthyRequestForwarded covers FR-1: a request that arrives while the
// target is believed healthy is forwarded directly, with no wake and no wait.
func TestFR1HealthyRequestForwarded(t *testing.T) {
	const resp = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"
	addr := rawTarget(t, func(c net.Conn) {
		readRequest(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		io.WriteString(c, resp)
	})

	health := NewHealth()
	health.observe(true)
	pl := NewPipeline(context.Background(), time.Hour, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://"+addr), NewLogger(io.Discard))
	_, laddr := startListener(t, testConfig(), pl.Handle)

	conn := get(t, laddr)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(conn)
	if string(data) != resp {
		t.Fatalf("client received %q, want the target's %q", data, resp)
	}
}

// bootWindowTarget records when each upstream connection is accepted and answers
// each request with resp.
func bootWindowTarget(t *testing.T, want, resp string) (addr string, accepted func() []time.Time) {
	t.Helper()
	var mu sync.Mutex
	var times []time.Time
	addr = rawTarget(t, func(c net.Conn) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		readRequest(c, want)
		io.WriteString(c, resp)
	})
	return addr, func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), times...)
	}
}

// holdAndServe creates n held requests through the listener and returns the
// connections so the caller can flip health and read the responses.
func holdAndServe(t *testing.T, l *Listener, addr string, n int) []net.Conn {
	t.Helper()
	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, get(t, addr))
	}
	waitFor(t, "all requests held", func() bool { return l.heldCount() == n })
	return conns
}

// TestFR4AndFR5AllHeldRequestsForwardInParallel covers FR-4 and FR-5: three
// requests held during one boot window all forward once healthy, each on its own
// upstream connection, opened within 100 ms of each other.
func TestFR4AndFR5AllHeldRequestsForwardInParallel(t *testing.T) {
	const req = "GET / HTTP/1.1\r\nHost: x\r\n\r\n"
	const resp = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
	addr, accepted := bootWindowTarget(t, req, resp)

	health := NewHealth()
	prober, boot := bootingProber(t, health, 5*time.Millisecond)
	pl := NewPipeline(context.Background(), time.Hour, health,
		prober, wakeStub(t), testForwarder(t, "http://"+addr), NewLogger(io.Discard))
	l, laddr := startListener(t, testConfig(), pl.Handle)

	const n = 3
	conns := holdAndServe(t, l, laddr, n)

	boot() // the boot finishes; the prober's next probe observes ready

	for _, c := range conns {
		defer c.Close()
		if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(c)
		if string(data) != resp {
			t.Fatalf("client received %q, want %q", data, resp)
		}
	}

	times := accepted()
	if len(times) != n {
		t.Fatalf("target accepted %d connections, want %d", len(times), n)
	}
	span := times[len(times)-1].Sub(times[0])
	if span >= 100*time.Millisecond {
		t.Fatalf("upstream connections spanned %v, want under 100ms (FR-5)", span)
	}
}

// TestNFR5EightConcurrentAllGetResponse covers NFR-5: eight concurrent requests
// during one boot window are all held without being discarded and all receive
// the target's response.
func TestNFR5EightConcurrentAllGetResponse(t *testing.T) {
	const req = "GET / HTTP/1.1\r\nHost: x\r\n\r\n"
	const resp = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
	addr, _ := bootWindowTarget(t, req, resp)

	health := NewHealth()
	prober, boot := bootingProber(t, health, 5*time.Millisecond)
	pl := NewPipeline(context.Background(), time.Hour, health,
		prober, wakeStub(t), testForwarder(t, "http://"+addr), NewLogger(io.Discard))
	l, laddr := startListener(t, testConfig(), pl.Handle)

	const n = 8
	conns := holdAndServe(t, l, laddr, n)
	boot()

	for _, c := range conns {
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		data, _ := io.ReadAll(c)
		if string(data) != resp {
			t.Fatalf("client received %q, want %q", data, resp)
		}
	}
}

// TestFR18TransportFailureGives502 covers FR-18: a forward that cannot reach the
// target returns one 502 naming the target.
func TestFR18TransportFailureGives502(t *testing.T) {
	health := NewHealth()
	health.observe(true)
	pl := NewPipeline(context.Background(), time.Hour, health,
		notReadyProber(t, health), wakeStub(t), testForwarder(t, "http://"+closedAddr(t)), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)
	h.wait(t)
	status, detail := parseErrorResponse(t, h.response(t, p))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if detail.Component != componentTarget {
		t.Fatalf("502 body does not name the target: %+v", detail)
	}
}

// TestFR19TransportFailureTriggersProbe covers FR-19: a transport-level forward
// failure probes the target and sets the belief from the result, rather than
// inferring it from the failure.
func TestFR19TransportFailureTriggersProbe(t *testing.T) {
	health := NewHealth()
	health.observe(true)

	var calls atomic.Int64
	prober := stubProber(health, time.Hour, func(context.Context) bool {
		calls.Add(1)
		return false
	})
	t.Cleanup(prober.Close)

	pl := NewPipeline(context.Background(), time.Hour, health,
		prober, wakeStub(t), testForwarder(t, "http://"+closedAddr(t)), NewLogger(io.Discard))

	p, client := newPipePending(t)
	h := startHandle(pl, p, client)
	h.wait(t)
	h.response(t, p)

	if got := calls.Load(); got != 1 {
		t.Fatalf("probe ran %d time(s) after the failure, want 1", got)
	}
	if health.Healthy() {
		t.Fatal("the belief did not follow the probe result (FR-19)")
	}
}

// TestForwardNoTimeoutInSource keeps §8.5's "no connection timeout of its own"
// true for the forward path: no dial timeout and no deadline.
func TestForwardNoTimeoutInSource(t *testing.T) {
	data, err := os.ReadFile("forward.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"DialTimeout", "SetDeadline", "SetReadDeadline", "SetWriteDeadline"} {
		if bytes.Contains(data, []byte(banned)) {
			t.Errorf("forward.go contains %s, which §8.5 forbids", banned)
		}
	}
}
