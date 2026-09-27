// Package httpsendobserver exposes a request-local observation point immediately
// before a physical HTTP transport send. It does not alter headers or bodies.
package httpsendobserver

import (
	"context"
	"net/http"
	"strings"
)

type contextKey struct{}

// WithObserver attaches an observer that must not retain or mutate the request.
func WithObserver(ctx context.Context, observer func(*http.Request)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, observer)
}

// Enabled reports whether a non-WebSocket request has a send observer.
func Enabled(request *http.Request) bool {
	if request == nil {
		return false
	}
	for name, values := range request.Header {
		if !strings.EqualFold(name, "Upgrade") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				if strings.EqualFold(strings.TrimSpace(token), "websocket") {
					return false
				}
			}
		}
	}
	observer, _ := request.Context().Value(contextKey{}).(func(*http.Request))
	return observer != nil
}

// Notify is called by dispatchers only after local admission and transport
// selection succeed, immediately before delegating the physical send.
func Notify(request *http.Request) {
	if !Enabled(request) {
		return
	}
	if observer, ok := request.Context().Value(contextKey{}).(func(*http.Request)); ok && observer != nil {
		observer(request)
	}
}

type transport struct{ next http.RoundTripper }

// Wrap observes each RoundTrip, including individual redirect sends.
func Wrap(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &transport{next: next}
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	Notify(request)
	return t.next.RoundTrip(request)
}

func (t *transport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
