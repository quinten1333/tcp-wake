package proxy

import "sync"

// heldSet is the in-memory set of requests currently held. It is unbounded by
// design (ADR-0012) and dies with the process (FR-16). Access is serialised;
// every method is safe for concurrent use by the per-connection goroutines and
// the close watchers. It is unexported because it is an implementation detail
// of the Listener: nothing outside this package observes the set directly.
type heldSet struct {
	mu    sync.Mutex
	items map[*Pending]struct{}
}

// newHeldSet returns an empty held set.
func newHeldSet() *heldSet {
	return &heldSet{items: make(map[*Pending]struct{})}
}

// add inserts a request into the set. Adding the same request twice is a no-op.
func (h *heldSet) add(p *Pending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items[p] = struct{}{}
}

// remove takes a request out of the set. Removing an absent request is a no-op.
func (h *heldSet) remove(p *Pending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.items, p)
}

// count reports how many requests are held.
func (h *heldSet) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items)
}

// closeAll abandons every held request and closes its client connection. It is
// used on shutdown and drops the set before touching the connections, so a
// close watcher racing to remove never blocks the drain.
func (h *heldSet) closeAll() {
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
