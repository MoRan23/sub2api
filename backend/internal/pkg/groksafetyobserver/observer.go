// Package groksafetyobserver connects request-local safety evidence to physical
// HTTP attempts without coupling the transport layer to gateway services.
package groksafetyobserver

import (
	"context"
	"net/http"
)

type Observer interface {
	ObserveResponse(*http.Response)
	Selected()
	Rejected() bool
}

type factoryKey struct{}
type responseKey struct{}
type enabledKey struct{}

// Enable marks a concrete business request. Merely inheriting a policy holder
// must not turn token refresh or catalog queries into chargeable attempts.
func Enable(req *http.Request) *http.Request {
	if req == nil {
		return nil
	}
	return req.WithContext(context.WithValue(req.Context(), enabledKey{}, true))
}

func WithFactory(ctx context.Context, factory func(*http.Request) Observer) context.Context {
	return context.WithValue(ctx, factoryKey{}, factory)
}

func Begin(req *http.Request) Observer {
	if req == nil {
		return nil
	}
	if enabled, _ := req.Context().Value(enabledKey{}).(bool); !enabled {
		return nil
	}
	f, _ := req.Context().Value(factoryKey{}).(func(*http.Request) Observer)
	if f == nil {
		return nil
	}
	return f(req)
}

func Attach(req *http.Request, resp *http.Response, observer Observer) {
	if resp == nil || observer == nil {
		return
	}
	r := resp.Request
	if r == nil {
		r = req
	}
	if r == nil {
		return
	}
	resp.Request = r.WithContext(context.WithValue(r.Context(), responseKey{}, observer))
	observer.ObserveResponse(resp)
}

func FromResponse(resp *http.Response) Observer {
	if resp == nil || resp.Request == nil {
		return nil
	}
	o, _ := resp.Request.Context().Value(responseKey{}).(Observer)
	return o
}

func Select(resp *http.Response) {
	if o := FromResponse(resp); o != nil {
		o.Selected()
	}
}

func Rejected(resp *http.Response) bool {
	o := FromResponse(resp)
	return o != nil && o.Rejected()
}

// RoundTrip observes one physical attempt. A caller performing a compatibility
// fallback must call it separately and select the response actually returned.
func RoundTrip(base http.RoundTripper, req *http.Request) (*http.Response, error) {
	o := Begin(req)
	resp, err := base.RoundTrip(req)
	Attach(req, resp, o)
	return resp, err
}
