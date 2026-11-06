package runtimeobs

import (
	"context"
	"io"
	"net/http"
	"sync"
)

type bodyScopeKey struct{}

// bodyScope counts the logical inbound body once, including prereads and
// decompression. It never retains body bytes or the request/context itself.
type bodyScope struct {
	mu       sync.Mutex
	counters *Counters
	size     int64
	wireSize int64
	closed   bool
}

type observedBody struct {
	io.ReadCloser
	scope *bodyScope
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.scope.mu.Lock()
		b.scope.wireSize += int64(n)
		b.scope.observeLocked(b.scope.wireSize)
		b.scope.mu.Unlock()
	}
	return n, err
}

// TrackRequestBody installs a numeric lifetime scope. Nested body-limit
// middleware reuses the same scope; only its original owner releases it.
func (c *Counters) TrackRequestBody(req *http.Request) (*http.Request, func()) {
	if req == nil || req.Body == nil || req.Context().Value(bodyScopeKey{}) != nil {
		return req, func() {}
	}
	scope := &bodyScope{counters: c}
	ctx := context.WithValue(req.Context(), bodyScopeKey{}, scope)
	next := req.WithContext(ctx)
	next.Body = &observedBody{ReadCloser: req.Body, scope: scope}
	return next, scope.finish
}

// ObserveBufferedBody includes decoded/normalized bytes without counting a
// second copy when the same immutable ingress body is read more than once.
func ObserveBufferedBody(ctx context.Context, size int64) {
	if ctx == nil {
		return
	}
	if scope, ok := ctx.Value(bodyScopeKey{}).(*bodyScope); ok {
		scope.mu.Lock()
		scope.observeLocked(size)
		scope.mu.Unlock()
	}
}

func (s *bodyScope) observeLocked(size int64) {
	if s.closed || size <= s.size {
		return
	}
	if s.size == 0 {
		s.counters.bodyCount.Add(1)
	}
	s.counters.bodyBytes.Add(size - s.size)
	if size >= LargeBodyThreshold {
		if s.size < LargeBodyThreshold {
			s.counters.largeCount.Add(1)
			s.counters.largeBytes.Add(size)
		} else {
			s.counters.largeBytes.Add(size - s.size)
		}
	}
	s.size = size
}

func (s *bodyScope) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.size > 0 {
		s.counters.bodyCount.Add(-1)
		s.counters.bodyBytes.Add(-s.size)
		s.counters.bodySizes.Observe(uint64(s.size), SizeBounds)
		if s.size >= LargeBodyThreshold {
			s.counters.largeCount.Add(-1)
			s.counters.largeBytes.Add(-s.size)
		}
	}
}
