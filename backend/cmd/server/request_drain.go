package main

import (
	"net/http"
	"sync"
)

// requestDrain keeps storage alive until request handlers have submitted their
// final billing work, including handlers on hijacked WebSocket connections.
// Admission and Wait are separated so no positive Add can race with shutdown.
type requestDrain struct {
	next   http.Handler
	mu     sync.RWMutex
	closed bool
	active sync.WaitGroup
}

func (d *requestDrain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	if d.closed {
		d.mu.RUnlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	d.active.Add(1)
	d.mu.RUnlock()
	defer d.active.Done()
	d.next.ServeHTTP(w, r)
}

func (d *requestDrain) seal() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
}

func (d *requestDrain) wait() { d.active.Wait() }
