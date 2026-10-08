package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// countingPendingRaw is countingPending for a caller-chosen request, so a test
// can put a sentinel token in the request body and prove it never reaches the
// log.
func countingPendingRaw(t *testing.T, raw string) (*Pending, *responseCounter) {
	t.Helper()
	client, server := net.Pipe()
	counter := &responseCounter{Conn: server}
	go io.Copy(io.Discard, client)
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return newPending(counter, bufio.NewReader(server), []byte(raw)), counter
}

// postWithBody builds a complete request carrying body, so a test can watch for
// its bytes in the log.
func postWithBody(body string) string {
	return "POST /v1/chat/completions HTTP/1.1\r\nHost: x\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

func wakeLineCount(s string) int  { return strings.Count(s, "wake command=") }
func errorLineCount(s string) int { return strings.Count(s, " error status=") }

// TestLoggerErrorLine covers ADR-0010's error line: one line per call, with a
// timestamp, the status the client was given, and the enumerable component.
func TestLoggerErrorLine(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	logger.Error(500, errorDetail{Message: "wake failed", Component: componentWakeCommand})
	logger.Error(504, errorDetail{Message: "too slow", Component: componentWaitBound, Limit: "2m0s"})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d log lines, want 2 (ADR-0010):\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, "error status=") || !strings.Contains(line, "component=") || !strings.Contains(line, "message=") {
			t.Fatalf("error line missing a field: %q", line)
		}
		if _, err := time.Parse(time.RFC3339, strings.Fields(line)[0]); err != nil {
			t.Fatalf("log line does not start with an RFC3339 timestamp: %q", line)
		}
	}
	if !strings.Contains(lines[0], "status=500 component=wake_command") {
		t.Errorf("first line does not name the wake command: %q", lines[0])
	}
	if !strings.Contains(lines[1], "status=504 component=wait_bound") {
		t.Errorf("second line does not name the wait bound: %q", lines[1])
	}
}

// errWriter always fails, standing in for an unwritable log.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("log is not writable") }

// TestLoggerReady covers SRS §5.6: an unwritable log is a start-time failure.
func TestLoggerReady(t *testing.T) {
	if err := NewLogger(io.Discard).Ready(); err != nil {
		t.Errorf("Ready() on a writable log = %v, want nil", err)
	}
	if err := NewLogger(errWriter{}).Ready(); err == nil {
		t.Error("Ready() on a failing writer = nil, want an error")
	}
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := NewLogger(f).Ready(); err == nil {
		t.Error("Ready() on a closed file = nil, want an error")
	}
}

// TestADR0010OneWakeAndOneErrorLine covers the two line forms together, through
// the real pipeline: one wake line per execution and one error line per error,
// and nothing when a healthy request is relayed. The healthy case is also the
// FR-9 statement: traffic the system forwards produces zero wake executions.
func TestADR0010OneWakeAndOneErrorLine(t *testing.T) {
	const waitBound = 150 * time.Millisecond

	cases := []struct {
		name     string
		pl       func(t *testing.T, log *syncBuffer) *Pipeline
		wantWake int
		wantErr  int
	}{
		{
			"500 wake_command",
			func(t *testing.T, log *syncBuffer) *Pipeline {
				health := NewHealth()
				return NewPipeline(context.Background(), time.Hour, health,
					notReadyProber(t, health), &WakeTrigger{command: failingCommand(t, 3)},
					testForwarder(t, "http://127.0.0.1:1"), NewLogger(log))
			},
			1, 1,
		},
		{
			"502 target",
			func(t *testing.T, log *syncBuffer) *Pipeline {
				health := NewHealth()
				health.observe(true)
				pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
				t.Cleanup(pr.Close)
				return NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
					testForwarder(t, "http://"+postConnectFailureTarget(t)), NewLogger(log))
			},
			0, 1,
		},
		{
			"504 wait_bound",
			func(t *testing.T, log *syncBuffer) *Pipeline {
				health := NewHealth()
				return NewPipeline(context.Background(), waitBound, health,
					notReadyProber(t, health), wakeStub(t),
					testForwarder(t, "http://127.0.0.1:1"), NewLogger(log))
			},
			1, 1,
		},
		{
			"healthy relay",
			func(t *testing.T, log *syncBuffer) *Pipeline {
				addr := rawTarget(t, func(c net.Conn) {
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
				})
				health := NewHealth()
				health.observe(true)
				pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
				t.Cleanup(pr.Close)
				return NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
					testForwarder(t, "http://"+addr), NewLogger(log))
			},
			0, 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &syncBuffer{}
			pl := tc.pl(t, log)
			p, _ := countingPending(t)
			runHandle(t, pl, p)
			if got := wakeLineCount(log.String()); got != tc.wantWake {
				t.Errorf("wrote %d wake lines, want %d:\n%s", got, tc.wantWake, log.String())
			}
			if got := errorLineCount(log.String()); got != tc.wantErr {
				t.Errorf("wrote %d error lines, want %d:\n%s", got, tc.wantErr, log.String())
			}
		})
	}

	t.Run("413 held_body_cap", func(t *testing.T) {
		log := &syncBuffer{}
		cfg := testConfig()
		cfg.HeldBodyCap = 10
		_, addr := startListenerLogged(t, cfg, func(*Pending) {
			t.Error("an over-cap request must not reach the handler")
		}, NewLogger(log))

		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 11\r\n\r\n"); err != nil {
			t.Fatalf("write request: %v", err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		io.ReadAll(conn)

		waitFor(t, "413 log line", func() bool { return errorLineCount(log.String()) == 1 })
		if !strings.Contains(log.String(), "status=413 component=held_body_cap") {
			t.Fatalf("413 line does not name the cap:\n%s", log.String())
		}
		if got := wakeLineCount(log.String()); got != 0 {
			t.Errorf("wrote %d wake lines for a rejected body, want 0", got)
		}
	})
}

// assertNoToken fails if the log carries a request or response sentinel. This
// is the ADR-0010 inspection as an executable check.
func assertNoToken(t *testing.T, log, token string) {
	t.Helper()
	if strings.Contains(log, token) {
		t.Fatalf("the log carries request/response content %q (§2 non-goal 3):\n%s", token, log)
	}
}

// TestLoggerNoRequestOrResponseContent drives real request and response bodies
// carrying unique tokens and asserts neither reaches the log, on a path where a
// line IS written (502) and on a relay of a target body.
func TestLoggerNoRequestOrResponseContent(t *testing.T) {
	const reqToken = "REQ_SENTINEL_4f2a"
	const respToken = "RESP_SENTINEL_9c7b"
	req := postWithBody("prompt=" + reqToken)

	t.Run("error line names no content", func(t *testing.T) {
		log := &syncBuffer{}
		health := NewHealth()
		health.observe(true)
		pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
		defer pr.Close()
		pl := NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
			testForwarder(t, "http://"+postConnectFailureTarget(t)), NewLogger(log))

		p, _ := countingPendingRaw(t, req)
		runHandle(t, pl, p)

		if errorLineCount(log.String()) != 1 {
			t.Fatalf("want one error line, got:\n%s", log.String())
		}
		assertNoToken(t, log.String(), reqToken)
	})

	t.Run("relayed response body is not logged", func(t *testing.T) {
		log := &syncBuffer{}
		addr := rawTarget(t, func(c net.Conn) {
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: "+strconv.Itoa(len(respToken))+"\r\n\r\n"+respToken)
		})
		health := NewHealth()
		health.observe(true)
		pr := stubProber(health, time.Hour, func(context.Context) bool { return false })
		defer pr.Close()
		pl := NewPipeline(context.Background(), time.Hour, health, pr, wakeStub(t),
			testForwarder(t, "http://"+addr), NewLogger(log))

		p, counter := countingPendingRaw(t, req)
		runHandle(t, pl, p)

		if !bytes.Contains(counter.raw(), []byte(respToken)) {
			t.Fatal("the target's body was not relayed to the client")
		}
		assertNoToken(t, log.String(), reqToken)
		assertNoToken(t, log.String(), respToken)
	})
}

// TestLoggerNeverTakesContent is the structural guard behind §2 non-goal 3: no
// Logger method accepts request or response bytes, so content cannot reach the
// log through the API.
func TestLoggerNeverTakesContent(t *testing.T) {
	data, err := os.ReadFile("log.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"[]byte", "*Pending", "net.Conn"}
	for _, b := range banned {
		if bytes.Contains(data, []byte(b)) {
			t.Errorf("log.go mentions %q, so the Logger could take request/response content", b)
		}
	}
}

// TestFR12NoLogLinesWhileIdle covers FR-12 through the log: with the listener
// running and no request pending, no wake or error line is written.
func TestFR12NoLogLinesWhileIdle(t *testing.T) {
	log := &syncBuffer{}
	health := NewHealth()
	pl := NewPipeline(context.Background(), time.Hour, health,
		notReadyProber(t, health), wakeStub(t),
		testForwarder(t, "http://127.0.0.1:1"), NewLogger(log))
	_, _ = startListenerLogged(t, testConfig(), pl.Handle, NewLogger(log))

	time.Sleep(60 * time.Millisecond)

	if got := strings.Count(log.String(), "\n"); got != 0 {
		t.Fatalf("wrote %d log lines while idle, want 0 (FR-12):\n%s", got, log.String())
	}
}

// healthLineCount counts the target state-change lines, the counting artefact
// for ADR-0017's transitions.
func healthLineCount(s string) int { return strings.Count(s, "health state=") }

// TestADR0017LoggerHealthLine covers the line form: one line per call, a
// timestamp, and the new state, greppable.
func TestADR0017LoggerHealthLine(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	logger.Health(false)
	logger.Health(true)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d health lines, want 2 (ADR-0017):\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "health state=unhealthy") {
		t.Errorf("first line = %q, want the unhealthy state", lines[0])
	}
	if !strings.Contains(lines[1], "health state=healthy") {
		t.Errorf("second line = %q, want the healthy state", lines[1])
	}
	for _, line := range lines {
		if _, err := time.Parse(time.RFC3339, strings.Fields(line)[0]); err != nil {
			t.Fatalf("health line does not start with an RFC3339 timestamp: %q", line)
		}
	}
}

// TestADR0017HealthTransitionLoggedEndToEnd drives the whole path: a request is
// held while the target is not healthy, the target becomes ready, and exactly
// one healthy transition line is written.
func TestADR0017HealthTransitionLoggedEndToEnd(t *testing.T) {
	s := newSystem(t, systemOptions{})

	conn := s.send()
	s.waitHeld(1)
	s.target.setReady(true)
	readAll(t, conn)

	waitFor(t, "healthy transition line", func() bool { return healthLineCount(s.log.String()) == 1 })
	if !strings.Contains(s.log.String(), "health state=healthy") {
		t.Fatalf("no healthy state line after the target became ready:\n%s", s.log.String())
	}
}
