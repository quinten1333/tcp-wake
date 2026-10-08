package proxy

import (
	"context"
	"sync"
	"time"

	"tcp-wake/internal/config"
)

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
	// logger records a health-state transition (ADR-0017). It may be nil, in
	// which case transitions are not logged (test stubs).
	logger *Logger

	mu      sync.Mutex
	pending int
	running bool
	stop    chan struct{}
}

// NewProber returns a Prober that polls cfg.TargetAddress cfg.HealthPath every
// cfg.ProbeInterval with cfg.ProbeTimeout, writing the result to health and
// logging each belief change to logger (ADR-0017).
func NewProber(cfg *config.Config, health *Health, logger *Logger) *Prober {
	return &Prober{
		health:   health,
		interval: cfg.ProbeInterval,
		probeFn:  httpProbe(cfg.TargetAddress, cfg.HealthPath, cfg.ProbeTimeout),
		logger:   logger,
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
	pr.record(ready)
	return ready
}

// record observes a probe result and logs it when the belief changes, so a
// state-change line is written once per transition, not once per probe
// (ADR-0017). A nil logger is a test stub that does not log.
func (pr *Prober) record(ready bool) {
	if pr.health.observe(ready) && pr.logger != nil {
		pr.logger.Health(ready)
	}
}

// Observe records a health observation made outside the probe loop, such as the
// outcome of the request path's own connection attempt (ADR-0018), and logs a
// transition. It is the same seam the probes use, so the belief has one writer
// and one log line per change however it was observed.
func (pr *Prober) Observe(ready bool) {
	pr.record(ready)
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

		ready := pr.probeFn(context.Background())
		pr.record(ready)
		if ready {
			pr.mu.Lock()
			pr.finishLocked()
			pr.mu.Unlock()
			return
		}

		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
