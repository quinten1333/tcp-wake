package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"tcp-wake/internal/config"
)

// responseCounter records every byte the proxy writes on a connection so a test
// can count the responses it produced. That measurement is what NFR-6 needs:
// exactly one response per accepted request whose client stays connected.
// net.Pipe is synchronous, so the caller must drain the client end or a write
// blocks forever.
type responseCounter struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *responseCounter) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.mu.Lock()
	c.buf.Write(p[:n])
	c.mu.Unlock()
	return n, err
}

func (c *responseCounter) raw() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

// recordedResponse is one HTTP response the proxy wrote, with its body read so
// the next ReadResponse starts at the next response.
type recordedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// responses parses the recorded bytes as a sequence of HTTP responses. It fails
// on trailing or unparseable bytes, so a relayed body that merely contains the
// text "HTTP/1.1" is not miscounted as a second response.
func (c *responseCounter) responses(t *testing.T) []recordedResponse {
	t.Helper()
	data := c.raw()
	var out []recordedResponse
	r := bufio.NewReader(bytes.NewReader(data))
	for {
		if _, err := r.Peek(1); err == io.EOF {
			break
		}
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatalf("proxy wrote bytes that are not a response: %v (bytes=%q)", err, data)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("reading recorded response body: %v", err)
		}
		out = append(out, recordedResponse{Status: resp.StatusCode, Header: resp.Header, Body: body})
	}
	return out
}

// countingPending returns a Pending whose writes are recorded by a
// responseCounter. The client end is drained so a synchronous net.Pipe write
// never blocks.
func countingPending(t *testing.T) (*Pending, *responseCounter) {
	t.Helper()
	client, server := net.Pipe()
	counter := &responseCounter{Conn: server}
	go io.Copy(io.Discard, client)
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	p := newPending(counter, bufio.NewReader(server), []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	return p, counter
}

// runHandle runs one Handle call to completion, failing rather than hanging.
func runHandle(t *testing.T, pl *Pipeline, p *Pending) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		pl.Handle(p)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle did not return")
	}
}

// oneResponse asserts the counter holds exactly one response and returns it.
// A second response would violate NFR-6 just as surely as none.
func oneResponse(t *testing.T, c *responseCounter) recordedResponse {
	t.Helper()
	resps := c.responses(t)
	if len(resps) != 1 {
		t.Fatalf("proxy wrote %d responses, want exactly 1 (NFR-6)", len(resps))
	}
	return resps[0]
}

// errorBody unmarshals a recorded response into the ADR-0014 schema.
func errorBody(t *testing.T, r recordedResponse) errorDetail {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(r.Body, &env); err != nil {
		t.Fatalf("response body is not the ADR-0014 JSON: %q", r.Body)
	}
	return env.Error
}

// TestWriteErrorIsSingleWellFormedResponse checks the seam itself: writeError
// emits one complete, self-describing response, which parseErrorResponse
// validates against the shared ADR-0009 framing.
func TestWriteErrorIsSingleWellFormedResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	go func() {
		writeError(server, http.StatusBadGateway, errorDetail{Message: "target down", Component: componentTarget})
		server.Close()
	}()

	data, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	status, detail := parseErrorResponse(t, data)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if detail.Component != componentTarget {
		t.Errorf("component = %q, want %q", detail.Component, componentTarget)
	}
}

// TestADR0014ErrorBodySchema triggers each of the four error paths and asserts
// the exact body shape ADR-0014 fixes: status, enumerable component, non-empty
// message, and a limit present only for the two bounded conditions.
func TestADR0014ErrorBodySchema(t *testing.T) {
	const waitBound = 250 * time.Millisecond

	t.Run("504 wait_bound", func(t *testing.T) {
		health := NewHealth()
		pl := NewPipeline(context.Background(), waitBound, health,
			notReadyProber(t, health), wakeStub(t),
			testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))
		p, counter := countingPending(t)

		runHandle(t, pl, p)

		r := oneResponse(t, counter)
		if r.Status != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504", r.Status)
		}
		detail := errorBody(t, r)
		if detail.Component != componentWaitBound {
			t.Errorf("component = %q, want %q", detail.Component, componentWaitBound)
		}
		if detail.Message == "" {
			t.Error("message is empty")
		}
		if got, err := time.ParseDuration(detail.Limit); err != nil || got != waitBound {
			t.Errorf("limit = %q, want the configured bound %s (err=%v)", detail.Limit, waitBound, err)
		}
	})

	t.Run("500 wake_command", func(t *testing.T) {
		health := NewHealth()
		cfg := testConfig()
		cfg.WakeCommand = failingCommand(t, 3)
		pl := NewPipeline(context.Background(), time.Hour, health,
			notReadyProber(t, health), NewWakeTrigger(cfg),
			testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))
		p, counter := countingPending(t)

		runHandle(t, pl, p)

		r := oneResponse(t, counter)
		if r.Status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", r.Status)
		}
		detail := errorBody(t, r)
		if detail.Component != componentWakeCommand {
			t.Errorf("component = %q, want %q", detail.Component, componentWakeCommand)
		}
		if detail.Message == "" {
			t.Error("message is empty")
		}
		if !strings.Contains(detail.Message, cfg.WakeCommand) {
			t.Errorf("message %q does not name the wake command %q", detail.Message, cfg.WakeCommand)
		}
		if detail.Limit != "" {
			t.Errorf("limit = %q, want empty for an unbounded condition", detail.Limit)
		}
	})

	t.Run("502 target", func(t *testing.T) {
		health := NewHealth()
		health.observe(true)
		pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
		defer pr.Close()
		pl := NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
			testForwarder(t, "http://"+closedAddr(t)), NewLogger(io.Discard))
		p, counter := countingPending(t)

		runHandle(t, pl, p)

		r := oneResponse(t, counter)
		if r.Status != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", r.Status)
		}
		detail := errorBody(t, r)
		if detail.Component != componentTarget {
			t.Errorf("component = %q, want %q", detail.Component, componentTarget)
		}
		if detail.Message == "" {
			t.Error("message is empty")
		}
		if detail.Limit != "" {
			t.Errorf("limit = %q, want empty for an unbounded condition", detail.Limit)
		}
	})

	t.Run("413 held_body_cap", func(t *testing.T) {
		const cap = config.ByteSize(10)
		client, server := net.Pipe()
		counter := &responseCounter{Conn: server}
		go io.Copy(io.Discard, client)
		t.Cleanup(func() {
			client.Close()
			server.Close()
		})
		go io.WriteString(client, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 11\r\n\r\n")

		errCh := make(chan error, 1)
		go func() {
			_, err := Intake(counter, bufio.NewReader(counter), cap)
			errCh <- err
		}()
		if err := <-errCh; err == nil {
			t.Fatal("Intake accepted a body over the cap")
		}

		r := oneResponse(t, counter)
		if r.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", r.Status)
		}
		detail := errorBody(t, r)
		if detail.Component != componentHeldBodyCap {
			t.Errorf("component = %q, want %q", detail.Component, componentHeldBodyCap)
		}
		if detail.Message == "" {
			t.Error("message is empty")
		}
		if detail.Limit != cap.String() {
			t.Errorf("limit = %q, want the configured cap %q", detail.Limit, cap.String())
		}
	})
}

// TestNFR6ExactlyOneResponsePerAcceptedRequest is the countable form of NFR-6:
// every accepted request whose client stays connected gets exactly one
// response, and a request abandoned while held gets none. The total equals the
// accepted count minus the abandoned ones.
func TestNFR6ExactlyOneResponsePerAcceptedRequest(t *testing.T) {
	accepted := 0
	responded := 0

	// Each connected case must end in exactly one response.
	connected := []struct {
		name string
		pl   func(t *testing.T) *Pipeline
	}{
		{"504 wait_bound", func(t *testing.T) *Pipeline {
			health := NewHealth()
			return NewPipeline(context.Background(), 150*time.Millisecond, health,
				notReadyProber(t, health), wakeStub(t),
				testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))
		}},
		{"500 wake_command", func(t *testing.T) *Pipeline {
			health := NewHealth()
			cfg := testConfig()
			cfg.WakeCommand = failingCommand(t, 3)
			return NewPipeline(context.Background(), time.Hour, health,
				notReadyProber(t, health), NewWakeTrigger(cfg),
				testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))
		}},
		{"502 target", func(t *testing.T) *Pipeline {
			health := NewHealth()
			health.observe(true)
			pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
			t.Cleanup(pr.Close)
			return NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
				testForwarder(t, "http://"+closedAddr(t)), NewLogger(io.Discard))
		}},
		{"relayed target response", func(t *testing.T) *Pipeline {
			addr := rawTarget(t, func(c net.Conn) {
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			})
			health := NewHealth()
			health.observe(true)
			pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
			t.Cleanup(pr.Close)
			return NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
				testForwarder(t, "http://"+addr), NewLogger(io.Discard))
		}},
	}

	for _, tc := range connected {
		t.Run(tc.name, func(t *testing.T) {
			pl := tc.pl(t)
			p, counter := countingPending(t)
			runHandle(t, pl, p)
			if got := len(counter.responses(t)); got != 1 {
				t.Fatalf("wrote %d responses, want exactly 1", got)
			}
			accepted++
			responded++
		})
	}

	t.Run("abandoned while held", func(t *testing.T) {
		health := NewHealth()
		pl := NewPipeline(context.Background(), time.Hour, health,
			notReadyProber(t, health), wakeStub(t),
			testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))
		p, counter := countingPending(t)

		done := make(chan struct{})
		go func() {
			pl.Handle(p)
			close(done)
		}()
		// The client goes away while the request is held: no response (FR-7).
		p.Discard()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Handle did not return after the client left")
		}
		if got := len(counter.responses(t)); got != 0 {
			t.Fatalf("wrote %d responses to an abandoned request, want 0", got)
		}
		accepted++ // accepted, but not counted as responded
	})

	if accepted == 0 {
		t.Fatal("the NFR-6 accounting exercised no requests")
	}
	if responded != accepted-1 {
		t.Fatalf("responded %d of %d accepted, want exactly one fewer than accepted (the abandoned request)", responded, accepted)
	}
}

// TestNFR6OnlyErrorsDotGoWritesAResponse is the structural guard: writeError is
// the only place a response of the system's own is written, so the "exactly one
// response" property cannot be bypassed by a second write site later.
func TestNFR6OnlyErrorsDotGoWritesAResponse(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "errors.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		if bytes.Contains(data, []byte(`"HTTP/1.1 `)) {
			t.Errorf("%s writes an HTTP status line outside errors.go, breaking the single-seam NFR-6 property", name)
		}
	}
	if checked == 0 {
		t.Fatal("source inspection checked no files")
	}
}
