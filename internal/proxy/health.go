package proxy

import (
	"context"
	"sync"
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

// observe sets the belief from a probe result and reports whether it changed.
// It wakes every waiter when it becomes healthy. It is unexported on purpose: a
// probe result is the only input the state accepts (ADR-0008). The caller that
// observes production state logs the transition (ADR-0017).
func (h *Health) observe(ready bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ready == h.healthy {
		return false
	}
	h.healthy = ready
	if ready {
		close(h.changed)
		h.changed = make(chan struct{})
	}
	return true
}

// WaitHealthyOr blocks until the belief is healthy, done is closed, or ctx is
// done. It returns true only for the healthy case. A nil done blocks forever,
// which the pipeline does not use but tests rely on.
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
