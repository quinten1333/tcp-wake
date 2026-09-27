package proxy

import "sync"

// HeldSet is the in-memory set of requests currently held. It is unbounded by
// design (ADR-0012) and dies with the process (FR-16). Access is serialised;
// every method is safe for concurrent use by the per-connection goroutines and
// the close watchers.
type HeldSet struct {
	mu    sync.Mutex
	items map[*Pending]struct{}
}

// NewHeldSet returns an empty held set.
func NewHeldSet() *HeldSet {
	return &HeldSet{items: make(map[*Pending]struct{})}
}

// Add inserts a request into the set. Adding the same request twice is a no-op.
func (h *HeldSet) Add(p *Pending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items[p] = struct{}{}
}

// Remove takes a request out of the set. Removing an absent request is a no-op.
func (h *HeldSet) Remove(p *Pending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.items, p)
}

// Len reports how many requests are held.
func (h *HeldSet) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items)
}

// All returns a snapshot of the held requests. The slice is a copy, so the
// caller may iterate without holding the lock and cannot mutate the set.
func (h *HeldSet) All() []*Pending {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Pending, 0, len(h.items))
	for p := range h.items {
		out = append(out, p)
	}
	return out
}

// CloseAll abandons every held request and closes its client connection. It is
// used on shutdown and drops the set before touching the connections, so a
// close watcher racing to Remove never blocks the drain.
func (h *HeldSet) CloseAll() {
	h.mu.Lock()
	items := make([]*Pending, 0, len(h.items))
	for p := range h.items {
		items = append(items, p)
	}
	h.items = make(map[*Pending]struct{})
	h.mu.Unlock()

	for _, p := range items {
		p.Discard()
		p.Conn.Close()
	}
}
