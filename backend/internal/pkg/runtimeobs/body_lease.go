package runtimeobs

import "sync"

// BodyTracker owns numeric leases for one connection. Pending and active WS
// creates have separate leases; retries reuse the active lease. No request,
// context, body slice, or error is retained.
type BodyTracker struct {
	mu       sync.Mutex
	counters *Counters
	leases   map[*BodyLease]struct{}
	current  *BodyLease
	closed   bool
}
type BodyLease struct {
	owner    *BodyTracker
	size     int64
	released bool
}

func NewBodyTracker(c *Counters) *BodyTracker {
	return &BodyTracker{counters: c, leases: make(map[*BodyLease]struct{})}
}
func (t *BodyTracker) beginLocked(size int64) *BodyLease {
	l := &BodyLease{owner: t, size: max(size, 0), released: t.closed}
	if t.closed {
		return l
	}
	t.leases[l] = struct{}{}
	t.counters.bodyCount.Add(1)
	t.counters.bodyBytes.Add(l.size)
	if l.size >= LargeBodyThreshold {
		t.counters.largeCount.Add(1)
		t.counters.largeBytes.Add(l.size)
	}
	return l
}
func (t *BodyTracker) Begin(size int64) *BodyLease {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.beginLocked(size)
}
func (t *BodyTracker) Activate(l *BodyLease) {
	if t == nil || l == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !l.released && l.owner == t {
		t.current = l
	}
}

// EnsureCurrent counts the first physical frame once, even across attempts.
func (t *BodyTracker) EnsureCurrent(size int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		t.current = t.beginLocked(size)
	} else {
		t.current.observeLocked(size)
	}
}
func (t *BodyTracker) releaseLocked(l *BodyLease) {
	if l == nil || l.released {
		return
	}
	l.released = true
	delete(t.leases, l)
	t.counters.bodyCount.Add(-1)
	t.counters.bodyBytes.Add(-l.size)
	t.counters.bodySizes.Observe(uint64(l.size), SizeBounds)
	if l.size >= LargeBodyThreshold {
		t.counters.largeCount.Add(-1)
		t.counters.largeBytes.Add(-l.size)
	}
	if t.current == l {
		t.current = nil
	}
}
func (t *BodyTracker) CompleteCurrent() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseLocked(t.current)
}
func (t *BodyTracker) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for l := range t.leases {
		t.releaseLocked(l)
	}
}
func (l *BodyLease) Release() {
	if l == nil {
		return
	}
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.owner.releaseLocked(l)
}
func (l *BodyLease) observeLocked(size int64) {
	if l.released || size <= l.size {
		return
	}
	c := l.owner.counters
	c.bodyBytes.Add(size - l.size)
	if size >= LargeBodyThreshold {
		if l.size < LargeBodyThreshold {
			c.largeCount.Add(1)
			c.largeBytes.Add(size)
		} else {
			c.largeBytes.Add(size - l.size)
		}
	}
	l.size = size
}
func (l *BodyLease) ObserveSize(size int64) {
	if l == nil {
		return
	}
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.observeLocked(size)
}
