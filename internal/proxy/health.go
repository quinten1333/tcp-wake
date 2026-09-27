package proxy

import (
	"context"
	"sync"
	"time"

	"tcp-wake/internal/config"
)

// Health is the belief about whether the target is ready. It is the two-state
// health state of §5.2: healthy or not healthy. The belief changes only from a
// probe observation (FR-10, FR-11, FR-19), so there is deliberately no exported
// setter: no other block can infer the state from, say, a failed forward
// (ADR-0008). Production code reaches observe through Prober alone.
type Health struct {
	mu      sync.Mutex
	healthy bool
	// changed is closed when the belief becomes healthy, waking every waiter.
	// It is replaced on each transition so a later transition can broadcast
	// again over a fresh channel.
	changed chan struct{}
}

// NewHealth returns a belief that starts not healthy, matching a fresh process
// (FR-16, §6.8).
func NewHealth() *Health {
	return &Health{changed: make(chan struct{})}
}

// Healthy reports the current belief.
func (h *Health) Healthy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.healthy
}

// observe sets the belief from a probe result and wakes every waiter when it
// becomes healthy. It is unexported on purpose: a probe result is the only
// input the state accepts (ADR-0008).
func (h *Health) observe(ready bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ready == h.healthy {
		return
	}
	h.healthy = ready
	if ready {
		close(h.changed)
		h.changed = make(chan struct{})
	}
}

// WaitHealthy blocks until the belief is healthy or ctx is done. It returns
// true only for the healthy case.
func (h *Health) WaitHealthy(ctx context.Context) bool {
	return h.WaitHealthyOr(ctx, nil)
}

// WaitHealthyOr blocks until the belief is healthy, done is closed, or ctx is
// done. It returns true only for the healthy case. A nil done blocks forever,
// which WaitHealthy relies on.
func (h *Health) WaitHealthyOr(ctx context.Context, done <-chan struct{}) bool {
	for {
		h.mu.Lock()
		if h.healthy {
			h.mu.Unlock()
			return true
		}
		changed := h.changed
		h.mu.Unlock()

		select {
		case <-changed:
			// Loop to re-read the belief; a transition to not healthy is not
			// a release.
		case <-done:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// Prober determines the target's health from its health endpoint and sets the
// belief from the result (ADR-0007, ADR-0008). It polls while at least one
// request is pending and once on demand after a transport-level forward
// failure (FR-19). It is the only writer of Health in production.
type Prober struct {
	health   *Health
	interval time.Duration
	// probeFn is the observation. It is a field so tests can replace the HTTP
	// probe with a stub; NewProber installs the real one.
	probeFn func(context.Context) bool

	mu      sync.Mutex
	pending int
	running bool
	stop    chan struct{}
}

// NewProber returns a Prober that polls cfg.TargetAddress cfg.HealthPath every
// cfg.ProbeInterval with cfg.ProbeTimeout, writing the result to health.
func NewProber(cfg *config.Config, health *Health) *Prober {
	return &Prober{
		health:   health,
		interval: cfg.ProbeInterval,
		probeFn:  httpProbe(cfg.TargetAddress, cfg.HealthPath, cfg.ProbeTimeout),
	}
}

// RequestStarted records that a request is pending. It starts the cadence loop
// only for the first pending request, and never while the target is already
// believed healthy: a healthy target is forwarded to directly, with no probe
// (ADR-0008, FR-12).
func (pr *Prober) RequestStarted() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.pending++
	if pr.pending == 1 && !pr.running && !pr.health.Healthy() {
		pr.startLocked()
	}
}

// RequestDone records that a request is no longer pending, stopping the loop
// once none remain (FR-12: no timer runs while nothing is pending).
func (pr *Prober) RequestDone() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.pending--
	if pr.pending == 0 && pr.running {
		pr.stopLocked()
	}
}

// ProbeNow runs a single probe and sets the belief from its result (FR-19).
// It is the path the forwarder calls after a transport-level failure; it does
// not start the cadence loop.
func (pr *Prober) ProbeNow(ctx context.Context) bool {
	ready := pr.probeFn(ctx)
	pr.health.observe(ready)
	return ready
}

// Close stops the cadence loop and releases its goroutine. It is idempotent
// and safe to call while the loop is probing.
func (pr *Prober) Close() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.running {
		pr.stopLocked()
	}
}

// startLocked launches the loop. The caller holds pr.mu.
func (pr *Prober) startLocked() {
	pr.running = true
	pr.stop = make(chan struct{})
	go pr.loop(pr.stop)
}

// stopLocked signals the loop to stop. The caller holds pr.mu. Setting running
// false before returning makes a second stopLocked a no-op, so the channel is
// closed exactly once even when RequestDone and Close race.
func (pr *Prober) stopLocked() {
	close(pr.stop)
	pr.stop = nil
	pr.running = false
}

// finishLocked clears the running flag after the loop exits on its own, for
// example on the first ready observation. The caller holds pr.mu.
func (pr *Prober) finishLocked() {
	pr.running = false
	pr.stop = nil
}

// loop probes immediately, then every interval, until the target is ready, the
// last pending request leaves, or Close is called. It exits on the first ready
// observation so a healthy target is never probed on a timer (ADR-0008).
func (pr *Prober) loop(stop chan struct{}) {
	ticker := time.NewTicker(pr.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		default:
		}

		if pr.probeFn(context.Background()) {
			pr.health.observe(true)
			pr.mu.Lock()
			pr.finishLocked()
			pr.mu.Unlock()
			return
		}
		pr.health.observe(false)

		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
