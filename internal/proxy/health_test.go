package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"tcp-wake/internal/config"
)

// probeConfig builds a config whose target is a live httptest server.
func probeConfig(targetURL string) *config.Config {
	return &config.Config{
		TargetAddress: targetURL,
		HealthPath:    "/health",
		ProbeInterval: 10 * time.Millisecond,
		ProbeTimeout:  250 * time.Millisecond,
	}
}

// readyHandler answers 200 with the ready body.
func readyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, readyBody)
	}
}

// stubProber builds a Prober whose observation is a stub, so cadence and gating
// are tested without a socket.
func stubProber(h *Health, interval time.Duration, fn func(context.Context) bool) *Prober {
	return &Prober{health: h, interval: interval, probeFn: fn}
}

// TestFR10ProbeReadySetsHealthy covers FR-10: a ready health endpoint within
// the probe timeout makes the state healthy.
func TestFR10ProbeReadySetsHealthy(t *testing.T) {
	srv := httptest.NewServer(readyHandler())
	defer srv.Close()

	h := NewHealth()
	pr := NewProber(probeConfig(srv.URL), h)

	if !pr.ProbeNow(context.Background()) {
		t.Fatal("ProbeNow reported not ready for a ready endpoint")
	}
	if !h.Healthy() {
		t.Fatal("state is not healthy after a ready probe (FR-10)")
	}
}

// TestFR11Probe503StaysNotHealthy covers FR-11: a non-ready status leaves the
// state not healthy.
func TestFR11Probe503StaysNotHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":{"message":"Loading model"}}`)
	}))
	defer srv.Close()

	h := NewHealth()
	pr := NewProber(probeConfig(srv.URL), h)

	if pr.ProbeNow(context.Background()) {
		t.Fatal("ProbeNow reported ready for a 503 endpoint")
	}
	if h.Healthy() {
		t.Fatal("state became healthy despite a 503 probe (FR-11)")
	}
}

// TestFR11ProbeTimeoutStaysNotHealthy covers FR-11: an endpoint slower than the
// probe timeout leaves the state not healthy.
func TestFR11ProbeTimeoutStaysNotHealthy(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	// Release the handler before closing the server, or Close waits forever
	// for the request the probe abandoned at its timeout.
	defer func() {
		close(release)
		srv.Close()
	}()

	cfg := probeConfig(srv.URL)
	cfg.ProbeTimeout = 30 * time.Millisecond
	h := NewHealth()
	pr := NewProber(cfg, h)

	start := time.Now()
	if pr.ProbeNow(context.Background()) {
		t.Fatal("ProbeNow reported ready for a slow endpoint")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probe took %v, want it bounded by the timeout", elapsed)
	}
	if h.Healthy() {
		t.Fatal("state became healthy despite a timed-out probe (FR-11)")
	}
}

// TestIF3ReadyRequiresStatusAndBody covers IF-3: the endpoint is ready only for
// HTTP 200 with the exact ready body.
func TestIF3ReadyRequiresStatusAndBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"200 ready body", http.StatusOK, readyBody, true},
		{"200 ready body with newline", http.StatusOK, readyBody + "\n", true},
		{"200 wrong body", http.StatusOK, `{"status":"loading"}`, false},
		{"200 empty body", http.StatusOK, "", false},
		{"503 ready body", http.StatusServiceUnavailable, readyBody, false},
		{"500 ready body", http.StatusInternalServerError, readyBody, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			h := NewHealth()
			pr := NewProber(probeConfig(srv.URL), h)
			if got := pr.ProbeNow(context.Background()); got != tc.want {
				t.Fatalf("ProbeNow = %v, want %v", got, tc.want)
			}
			if h.Healthy() != tc.want {
				t.Fatalf("Healthy = %v, want %v", h.Healthy(), tc.want)
			}
		})
	}
}

// TestIF3ProbeHitsConfiguredPath covers IF-3's configured endpoint: the probe
// requests health_path, not a hard-coded path.
func TestIF3ProbeHitsConfiguredPath(t *testing.T) {
	paths := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case paths <- r.URL.Path:
		default:
		}
		io.WriteString(w, readyBody)
	}))
	defer srv.Close()

	cfg := probeConfig(srv.URL)
	cfg.HealthPath = "/livez"
	h := NewHealth()
	pr := NewProber(cfg, h)
	pr.ProbeNow(context.Background())

	select {
	case p := <-paths:
		if p != "/livez" {
			t.Fatalf("probe hit %q, want the configured /livez", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe never reached the target")
	}
}

// TestFR12NoProbeWhileIdle covers FR-12 for the probe timer: while nothing is
// pending, no probe runs.
func TestFR12NoProbeWhileIdle(t *testing.T) {
	var calls atomic.Int64
	h := NewHealth()
	pr := stubProber(h, 5*time.Millisecond, func(context.Context) bool {
		calls.Add(1)
		return false
	})
	defer pr.Close()

	time.Sleep(60 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("ran %d probes while idle, want 0 (FR-12)", n)
	}
}

// TestProbeStopsWhenPendingDrops covers the pending gate: the cadence loop
// stops once the last pending request leaves.
func TestProbeStopsWhenPendingDrops(t *testing.T) {
	var calls atomic.Int64
	h := NewHealth()
	pr := stubProber(h, 5*time.Millisecond, func(context.Context) bool {
		calls.Add(1)
		return false
	})
	defer pr.Close()

	pr.RequestStarted()
	waitFor(t, "probes to start", func() bool { return calls.Load() > 0 })
	pr.RequestDone()

	time.Sleep(40 * time.Millisecond)
	settled := calls.Load()
	time.Sleep(40 * time.Millisecond)
	if got := calls.Load(); got != settled {
		t.Fatalf("kept probing after the last request left: %d -> %d", settled, got)
	}
}

// TestProbeLoopReleasesWaiterOnReady covers the release path: the cadence loop
// observes ready and wakes a goroutine blocked in WaitHealthy.
func TestProbeLoopReleasesWaiterOnReady(t *testing.T) {
	var calls atomic.Int64
	h := NewHealth()
	pr := stubProber(h, 5*time.Millisecond, func(context.Context) bool {
		return calls.Add(1) >= 3
	})
	defer pr.Close()

	pr.RequestStarted()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !h.WaitHealthy(ctx) {
		t.Fatal("waiter was not released by the ready observation")
	}
	pr.RequestDone()
}

// TestProbeLoopStopsOnceHealthy covers ADR-0008: once the target is believed
// healthy, no timer keeps probing even while requests are pending.
func TestProbeLoopStopsOnceHealthy(t *testing.T) {
	var calls atomic.Int64
	h := NewHealth()
	pr := stubProber(h, 5*time.Millisecond, func(context.Context) bool {
		return calls.Add(1) >= 2
	})
	defer pr.Close()

	pr.RequestStarted() // never done: pending stays > 0
	waitFor(t, "healthy", func() bool { return h.Healthy() })

	time.Sleep(30 * time.Millisecond)
	settled := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != settled {
		t.Fatalf("kept probing after healthy: %d -> %d", settled, got)
	}
}

// TestFR19ProbeNowSetsStateFromResult covers FR-19's observation: a single
// probe sets the state from its result, whatever the previous belief was. This
// is the call the forwarder makes after a transport-level failure.
func TestFR19ProbeNowSetsStateFromResult(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	h := NewHealth()
	pr := NewProber(probeConfig(down.URL), h)
	if pr.ProbeNow(context.Background()) {
		t.Fatal("probe against a 503 target reported ready")
	}

	up := httptest.NewServer(readyHandler())
	defer up.Close()
	pr.probeFn = httpProbe(up.URL, "/health", 250*time.Millisecond)
	if !pr.ProbeNow(context.Background()) {
		t.Fatal("probe against a ready target reported not ready")
	}
	if !h.Healthy() {
		t.Fatal("state did not follow the probe result (FR-19)")
	}
}

// TestHealthStateUnchangedWithoutProbe covers the ADR-0008 rule structurally:
// with no probe running, the belief never changes, so nothing infers the state
// from a failure's cause.
func TestHealthStateUnchangedWithoutProbe(t *testing.T) {
	h := NewHealth()
	time.Sleep(20 * time.Millisecond)
	if h.Healthy() {
		t.Fatal("state changed with no probe observation")
	}

	h.observe(true)
	time.Sleep(20 * time.Millisecond)
	if !h.Healthy() {
		t.Fatal("state changed back with no probe observation")
	}
}
