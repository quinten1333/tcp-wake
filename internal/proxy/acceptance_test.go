package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// This file is the T12 acceptance suite: one test named for each requirement
// ID, carrying the check from docs/specs/SRS.md. Tests already named for an ID
// in another file are not duplicated here; the requirement-to-test mapping is
// the RTM (docs/specs/RTM.md). The timing requirements (NFR-1, NFR-2, NFR-3)
// use scaled-down durations because the reference network (hyperion + hypha) is
// not available in the test environment; the shape of the check is preserved
// and the production default is asserted separately.

// TestFR2HoldsUntilHealthyAndSendsNoBytes covers FR-2: with the target off, one
// request returns no bytes before the target reports healthy.
func TestFR2HoldsUntilHealthyAndSendsNoBytes(t *testing.T) {
	s := newSystem(t, systemOptions{})
	conn := s.send()
	s.waitHeld(1)

	// While held, the client must receive nothing.
	conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	n, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("received %d bytes while the request was held, want none (FR-2)", n)
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("read returned %v, want a timeout with no bytes", err)
	}

	s.target.setReady(true)
	data := readAll(t, conn)
	if len(data) == 0 {
		t.Fatal("no response after the target became healthy")
	}
}

// TestFR9NoWakeForTrafficNotBoundForHypha covers FR-9 at the boundary the
// architecture assigns it: the routing layer (ADR-0002) sends only
// hypha-bound requests to this listener, so traffic to another vhost never
// reaches the system and produces no wake execution. The test drives that
// traffic at a separate target and asserts the system's log stays empty.
func TestFR9NoWakeForTrafficNotBoundForHypha(t *testing.T) {
	s := newSystem(t, systemOptions{})

	// Another vhost: a server the system was never configured to sit in front
	// of. A request to it must not touch the system's wake path.
	other := rawTarget(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	conn, err := net.Dial("tcp", other)
	if err != nil {
		t.Fatalf("dial other vhost: %v", err)
	}
	io.WriteString(conn, "GET /some/other/service HTTP/1.1\r\nHost: other\r\n\r\n")
	conn.SetReadDeadline(time.Now().Add(time.Second))
	io.ReadAll(conn)
	conn.Close()

	if got := s.wakeLogLines(); got != 0 {
		t.Fatalf("traffic to another vhost produced %d wake executions, want 0 (FR-9)", got)
	}
	if got := s.wakeExecutions(); got != 0 {
		t.Fatalf("wake command ran %d time(s) for another vhost, want 0 (FR-9)", got)
	}
}

// TestFR13ForwardedRequestBytesUnchanged covers FR-13 end to end: the bytes the
// target receives are the client's method, path, headers, and body, byte for
// byte, odd casing and order included.
func TestFR13ForwardedRequestBytesUnchanged(t *testing.T) {
	const req = "POST /v1/chat/completions?stream=1 HTTP/1.1\r\n" +
		"hOsT: hypha\r\n" +
		"CONTENT-length: 5\r\n" +
		"X-Odd-CASE:  Mixed   Value  \r\n" +
		"\r\n" +
		"hello"

	s := newSystem(t, systemOptions{})
	conn := s.sendRaw(req)
	s.waitHeld(1)
	s.target.setReady(true)
	readAll(t, conn)

	got := s.target.requests()
	if len(got) != 1 {
		t.Fatalf("target received %d requests, want 1", len(got))
	}
	if got[0] != req {
		t.Fatalf("forwarded request differs\n got: %q\nwant: %q", got[0], req)
	}
}

// TestFR14ResponseRelayedUnchanged covers FR-14: the response's status,
// headers, and body reach the client exactly as the target sent them.
func TestFR14ResponseRelayedUnchanged(t *testing.T) {
	const resp = "HTTP/1.1 201 Created\r\n" +
		"x-miXed: Yes\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Length: 11\r\n" +
		"\r\n" +
		"hello world"

	s := newSystem(t, systemOptions{})
	s.target.setForwardResponse(resp)
	s.target.setReady(true)

	conn := s.send()
	data := readAll(t, conn)
	if string(data) != resp {
		t.Fatalf("client received %q, want the target's %q", data, resp)
	}
}

// TestNFR1FirstResponseWithinBound covers NFR-1's shape with a scaled bound: a
// request received while the target is off gets its first response within the
// configured bound. The production 120 s default is asserted by the config
// test, because the reference network is not available here.
func TestNFR1FirstResponseWithinBound(t *testing.T) {
	const bound = 2 * time.Second
	s := newSystem(t, systemOptions{waitBound: bound})

	conn := s.send()
	s.waitHeld(1)

	// The target boots shortly after the request arrives.
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.target.setReady(true)
	}()

	start := time.Now()
	readAll(t, conn)
	if elapsed := time.Since(start); elapsed > bound {
		t.Fatalf("first response took %v, want within the %v bound (NFR-1)", elapsed, bound)
	}
}

// TestNFR2HealthyFirstResponseUnder2s covers NFR-2: a request that arrives while
// the target is believed healthy is forwarded directly and answered within 2 s.
func TestNFR2HealthyFirstResponseUnder2s(t *testing.T) {
	s := newSystem(t, systemOptions{})
	s.target.setReady(true)
	s.health.observe(true) // an already-healthy belief, as after a probe

	start := time.Now()
	conn := s.send()
	data := readAll(t, conn)
	elapsed := time.Since(start)

	if len(data) == 0 {
		t.Fatal("healthy request received no response")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("first response took %v, want under 2s while healthy (NFR-2)", elapsed)
	}
	if got := s.target.forwardConns(); got != 1 {
		t.Fatalf("target saw %d forwards, want 1", got)
	}
}

// TestNFR3StreamedChunkLatencyUnder50ms covers NFR-3: each streamed chunk is
// relayed as it arrives, adding no more than 50 ms. The target writes a chunk
// and waits for the test to release the next, so the measured gap is the relay
// latency, not the target's pacing.
func TestNFR3StreamedChunkLatencyUnder50ms(t *testing.T) {
	const n = 20
	acks := make([]chan struct{}, n)
	for i := range acks {
		acks[i] = make(chan struct{})
	}
	s := newSystem(t, systemOptions{})
	s.target.setReady(true)
	s.target.setForwardHandler(func(c net.Conn, req string) {
		io.WriteString(c, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
		for i := 0; i < n; i++ {
			body := fmt.Sprintf("chunk-%02d", i)
			fmt.Fprintf(c, "%x\r\n%s\r\n", len(body), body)
			<-acks[i]
		}
		io.WriteString(c, "0\r\n\r\n")
	})

	conn := s.send()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	if status := readResponseHead(t, r); status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	var last time.Time
	for i := 0; i < n; i++ {
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading chunk %d size: %v", i, err)
		}
		var size int
		fmt.Sscanf(strings.TrimSpace(sizeLine), "%x", &size)
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatalf("reading chunk %d body: %v", i, err)
		}
		r.ReadString('\n')
		if want := fmt.Sprintf("chunk-%02d", i); string(body) != want {
			t.Fatalf("chunk %d = %q, want %q", i, body, want)
		}
		now := time.Now()
		if !last.IsZero() {
			if gap := now.Sub(last); gap > 50*time.Millisecond {
				t.Fatalf("chunk %d arrived %v after the previous one, want under 50ms (NFR-3)", i, gap)
			}
		}
		last = now
		close(acks[i])
	}
	// Drain the terminating chunk and trailer so the relay can finish.
	r.ReadString('\n')
	for {
		line, err := r.ReadString('\n')
		if err != nil || line == "\r\n" || line == "\n" {
			break
		}
	}
	conn.Close()
}

// TestNFR8NoPrivilegeEscalationInSource is the code-side half of NFR-8's
// inspection: the proxy process never changes its uid, gid, or groups and never
// asks for elevated capabilities. The deployment half (the process runs as a
// non-root user with no added capabilities) is a documented checklist in the
// README, checked by scripts/check-deploy.sh.
func TestNFR8NoPrivilegeEscalationInSource(t *testing.T) {
	banned := []string{"Setuid", "Setgid", "Setgroups", "syscall.Credential", "CAP_"}
	scanNonTestSources(t, func(name string, data []byte) {
		for i, line := range strings.Split(string(data), "\n") {
			// Ignore comments: naming a capability is not requesting one.
			if j := strings.Index(line, "//"); j >= 0 {
				line = line[:j]
			}
			for _, b := range banned {
				if strings.Contains(line, b) {
					t.Errorf("%s:%d contains %s: the proxy must run without elevated privileges (NFR-8)", name, i+1, b)
				}
			}
		}
	})
}

// TestADR0018ReachableTargetForwardsWithoutWaking is the behaviour ADR-0018
// introduces: the belief starts not healthy (a fresh process), but a request is
// answered by trying the target first, so no wake runs when it is already up.
func TestADR0018ReachableTargetForwardsWithoutWaking(t *testing.T) {
	s := newSystem(t, systemOptions{})
	s.target.setReady(true) // the target is already on
	if s.health.Healthy() {
		t.Fatal("setup: a fresh system must start not healthy")
	}

	conn := s.send()
	data := readAll(t, conn)
	if len(data) == 0 {
		t.Fatal("a reachable target produced no response")
	}
	if got := s.wakeExecutions(); got != 0 {
		t.Fatalf("wake command ran %d time(s) though the target was reachable, want 0 (ADR-0018)", got)
	}
	if got := s.wakeLogLines(); got != 0 {
		t.Fatalf("wrote %d wake lines though the target was reachable, want 0", got)
	}
	if !s.health.Healthy() {
		t.Fatal("the successful attempt did not set the belief healthy")
	}
	if !strings.Contains(s.log.String(), "health state=healthy") {
		t.Fatalf("no healthy transition line was logged:\n%s", s.log.String())
	}
}

// TestADR0018UnreachableTargetHeldAndWoken covers the other half: a request to
// an off target is held, wakes it exactly once, and forwards once it is up.
func TestADR0018UnreachableTargetHeldAndWoken(t *testing.T) {
	s := newSystem(t, systemOptions{waitBound: time.Hour})

	conn := s.send()
	s.waitHeld(1)
	if got := s.target.forwardConns(); got != 0 {
		t.Fatalf("target saw %d forwards while it was off, want 0", got)
	}
	waitFor(t, "one wake line", func() bool { return s.wakeLogLines() == 1 })

	s.target.setReady(true)
	data := readAll(t, conn)
	if len(data) == 0 {
		t.Fatal("no response after the target became reachable")
	}
	if got := s.wakeLogLines(); got != 1 {
		t.Fatalf("wake lines = %d, want exactly 1", got)
	}
	if got := s.target.forwardConns(); got != 1 {
		t.Fatalf("target forwards = %d, want 1", got)
	}
}

// TestADR0018OutOfBandShutdownWakesInsteadOf502 covers the accepted risk R-8
// that ADR-0018 removes: a target that goes off after the belief became healthy
// no longer gives the request a 502; it is held and woken, and the belief logs
// the healthy-to-unhealthy transition.
func TestADR0018OutOfBandShutdownWakesInsteadOf502(t *testing.T) {
	s := newSystem(t, systemOptions{waitBound: time.Hour})

	// First request while the target is up: forwarded, belief healthy.
	s.target.setReady(true)
	first := s.send()
	readAll(t, first)
	if !s.health.Healthy() {
		t.Fatal("setup: the first request did not set the belief healthy")
	}

	// The target goes off out-of-band.
	s.target.setReady(false)

	// The next request must not 502: it is held and woken.
	second := s.send()
	s.waitHeld(1)
	waitFor(t, "unhealthy transition line", func() bool {
		return strings.Contains(s.log.String(), "health state=unhealthy")
	})
	if got := errorLineCount(s.log.String()); got != 0 {
		t.Fatalf("wrote %d error lines after an out-of-band shutdown, want 0 (R-8 removed)", got)
	}

	s.target.setReady(true)
	data := readAll(t, second)
	if len(data) == 0 {
		t.Fatal("the held request was not forwarded after the target came back")
	}
}
