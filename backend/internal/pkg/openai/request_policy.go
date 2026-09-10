package openai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/imroc/req/v3"
)

const CodexResidencyHeaderName = "x-openai-internal-codex-residency"

// RequestPolicy is frozen once per logical request, including credential retries.
type RequestPolicy struct {
	TimezoneConversionEnabled            bool
	PassthroughTimezoneConversionEnabled bool
	CodexResidencyUS                     bool
}

type requestPolicyContextKey struct{}
type residencyOriginContextKey struct{}

func DefaultRequestPolicy() RequestPolicy {
	return RequestPolicy{true, true, true}
}

func WithRequestPolicy(ctx context.Context, policy RequestPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestPolicyContextKey{}, policy)
}

func RequestPolicyFromContext(ctx context.Context) (RequestPolicy, bool) {
	if ctx != nil {
		if policy, ok := ctx.Value(requestPolicyContextKey{}).(RequestPolicy); ok {
			return policy, true
		}
	}
	return DefaultRequestPolicy(), false
}

func ApplyCodexResidencyHeader(header http.Header, enabled bool) {
	if !enabled || header == nil {
		return
	}
	removeCodexResidencyHeader(header)
	header.Set(CodexResidencyHeaderName, "us")
}

func removeCodexResidencyHeader(header http.Header) {
	for key := range header {
		if strings.EqualFold(key, CodexResidencyHeaderName) {
			delete(header, key)
		}
	}
}

// MarkCodexResidencyRequest identifies an explicitly routed OpenAI request. It
// does not classify arbitrary traffic by URL or by a shared transport profile.
func MarkCodexResidencyRequest(request *http.Request) *http.Request {
	if request == nil || request.URL == nil {
		return request
	}
	return request.WithContext(context.WithValue(request.Context(), residencyOriginContextKey{}, requestOrigin(request.URL)))
}

// IsCodexResidencyRequest reports explicit routing, independently of whether
// forcing the header is enabled. An existing header still needs redirect bounds.
func IsCodexResidencyRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	_, marked := request.Context().Value(residencyOriginContextKey{}).(string)
	return marked
}

func requestOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// HTTPClientWithCodexResidencyRedirectGuard copies only the client policy. The
// transport, connection pool, cookies and existing redirect decisions are kept.
func HTTPClientWithCodexResidencyRedirectGuard(client *http.Client) *http.Client {
	if client == nil {
		return nil
	}
	clone := *client
	previous := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		strip := func() {
			origin, marked := request.Context().Value(residencyOriginContextKey{}).(string)
			if marked && requestOrigin(request.URL) != origin {
				removeCodexResidencyHeader(request.Header)
			}
		}
		strip()
		if previous != nil {
			err := previous(request, via)
			strip()
			return err
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

// ReqClientWithRequestPolicy derives a request-scoped req client, sharing the
// original transport rather than changing a cached client's defaults.
func ReqClientWithRequestPolicy(client *req.Client, ctx context.Context) *req.Client {
	if client == nil {
		return nil
	}
	policy, _ := RequestPolicyFromContext(ctx)
	clone := client.Clone()
	clone.GetClient().Transport = client.GetClient().Transport
	clone.GetClient().Jar = client.GetClient().Jar
	guarded := HTTPClientWithCodexResidencyRedirectGuard(clone.GetClient())
	clone.GetClient().CheckRedirect = guarded.CheckRedirect
	clone.WrapRoundTripFunc(func(next req.RoundTripper) req.RoundTripFunc {
		return func(request *req.Request) (*req.Response, error) {
			request.SetContext(WithRequestPolicy(request.Context(), policy))
			request.SetContext(context.WithValue(request.Context(), residencyOriginContextKey{}, requestOrigin(request.URL)))
			ApplyCodexResidencyHeader(request.Headers, policy.CodexResidencyUS)
			return next.RoundTrip(request)
		}
	})
	return clone
}
