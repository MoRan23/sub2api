package codexnative

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const dispatcherCapacity = 256
const dispatcherIdleTTL = 15 * time.Minute

// Dispatcher selects from immutable native transports after all request headers
// have been merged. One dispatcher belongs to one proxy/timeout configuration.
// Account, purpose and profile remain separate even for platform-less SDK UAs.
type Dispatcher struct {
	fallback http.RoundTripper
	factory  func(Platform) (http.RoundTripper, error)
	mu       sync.Mutex
	entries  map[dispatchKey]*dispatchEntry
	capacity int
	ttl      time.Duration
	now      func() time.Time
}

type dispatchKey struct {
	accountID int64
	purpose   string
	digest    string
}

type dispatchEntry struct {
	transport http.RoundTripper
	active    int
	lastUsed  time.Time
}

func NewDispatcher(fallback http.RoundTripper, factory func(Platform) (http.RoundTripper, error)) *Dispatcher {
	if fallback == nil {
		fallback = http.DefaultTransport
	}
	return &Dispatcher{fallback: fallback, factory: factory, entries: make(map[dispatchKey]*dispatchEntry), capacity: dispatcherCapacity, ttl: dispatcherIdleTTL, now: time.Now}
}

func (d *Dispatcher) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("nil native HTTP request")
	}
	scope, enabled := ScopeFromContext(request.Context())
	if !enabled {
		return d.fallback.RoundTrip(request)
	}
	selection := Resolve(UserAgent(request.Header), scope)
	entry, err := d.acquire(dispatchKey{scope.AccountID, scope.Purpose, selection.Digest}, selection.Platform)
	if err != nil {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, err
	}
	response, err := entry.transport.RoundTrip(request)
	if err != nil || response == nil {
		d.release(entry)
		if err == nil {
			err = errors.New("native HTTP transport returned no response")
		}
		return response, err
	}
	if response.Body == nil {
		response.Body = http.NoBody
	}
	response.Body = &dispatchBody{ReadCloser: response.Body, release: func() { d.release(entry) }}
	return response, nil
}

func (d *Dispatcher) acquire(key dispatchKey, platform Platform) (*dispatchEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for k, entry := range d.entries {
		if entry.active == 0 && d.ttl > 0 && now.Sub(entry.lastUsed) >= d.ttl {
			closeIdle(entry.transport)
			delete(d.entries, k)
		}
	}
	if entry := d.entries[key]; entry != nil {
		entry.active++
		entry.lastUsed = now
		return entry, nil
	}
	if len(d.entries) >= d.capacity {
		var oldest *dispatchEntry
		var oldestKey dispatchKey
		for k, entry := range d.entries {
			if entry.active == 0 && (oldest == nil || entry.lastUsed.Before(oldest.lastUsed)) {
				oldest, oldestKey = entry, k
			}
		}
		if oldest == nil {
			return nil, errors.New("native HTTP client cache limit reached")
		}
		closeIdle(oldest.transport)
		delete(d.entries, oldestKey)
	}
	if d.factory == nil {
		return nil, errors.New("native HTTP transport factory unavailable")
	}
	transport, err := d.factory(platform)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("native HTTP transport factory returned nil")
	}
	entry := &dispatchEntry{transport: transport, active: 1, lastUsed: now}
	d.entries[key] = entry
	return entry, nil
}

func (d *Dispatcher) release(entry *dispatchEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry.active--
	entry.lastUsed = d.now()
}

// CloseIdleConnections retains active streams and reaches the real child pools.
func (d *Dispatcher) CloseIdleConnections() {
	d.mu.Lock()
	defer d.mu.Unlock()
	closeIdle(d.fallback)
	for key, entry := range d.entries {
		closeIdle(entry.transport)
		if entry.active == 0 {
			delete(d.entries, key)
		}
	}
}

func closeIdle(transport http.RoundTripper) {
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type dispatchBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *dispatchBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}

func (b *dispatchBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
