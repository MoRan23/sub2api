package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type validatorFunc func(context.Context, string) error
type validationContextKey struct{}

func (f validatorFunc) Validate(ctx context.Context, host string) error { return f(ctx, host) }
func (f validatorFunc) CloseIdleConnections()                           {}

func setTestValidatorFactory(t *testing.T, factory func(string) (resolvedIPValidator, error)) {
	t.Helper()
	original := newResolvedIPValidator
	newResolvedIPValidator = factory
	t.Cleanup(func() { newResolvedIPValidator = original })
}

func newTestValidatedTransport(t *testing.T, base http.RoundTripper, validate validatorFunc) *validatedTransport {
	t.Helper()
	setTestValidatorFactory(t, func(_ string) (resolvedIPValidator, error) { return validate, nil })
	transport, err := newValidatedTransport(base, "")
	require.NoError(t, err)
	return transport
}

func TestValidatedTransport_CacheHostValidation(t *testing.T) {
	var validateCalls int32
	validate := func(_ context.Context, host string) error {
		atomic.AddInt32(&validateCalls, 1)
		require.Equal(t, "api.openai.com", host)
		return nil
	}

	var baseCalls int32
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		atomic.AddInt32(&baseCalls, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})

	now := time.Unix(1730000000, 0)
	transport := newTestValidatedTransport(t, base, validate)
	transport.now = func() time.Time { return now }

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	require.Equal(t, int32(1), atomic.LoadInt32(&validateCalls))
	require.Equal(t, int32(2), atomic.LoadInt32(&baseCalls))
}

func TestValidatedTransport_ExpiredCacheTriggersRevalidation(t *testing.T) {
	var validateCalls int32
	validate := func(_ context.Context, _ string) error {
		atomic.AddInt32(&validateCalls, 1)
		return nil
	}

	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})

	now := time.Unix(1730001000, 0)
	transport := newTestValidatedTransport(t, base, validate)
	transport.now = func() time.Time { return now }

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	now = now.Add(validatedHostTTL + time.Second)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	require.Equal(t, int32(2), atomic.LoadInt32(&validateCalls))
}

func TestValidatedTransport_ValidationErrorStopsRoundTrip(t *testing.T) {
	expectedErr := errors.New("dns rebinding rejected")
	var validations int
	validate := func(_ context.Context, _ string) error {
		validations++
		return expectedErr
	}

	var baseCalls int32
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		atomic.AddInt32(&baseCalls, 1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})

	transport := newTestValidatedTransport(t, base, validate)
	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, expectedErr)
	require.Equal(t, int32(0), atomic.LoadInt32(&baseCalls))
	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, expectedErr)
	require.Equal(t, 2, validations, "failed validation is never cached")
}

func TestValidatedTransportBindsProxyAndSeparatesCaches(t *testing.T) {
	var checked []string
	setTestValidatorFactory(t, func(proxy string) (resolvedIPValidator, error) {
		return validatorFunc(func(ctx context.Context, host string) error {
			require.Equal(t, "request-context", ctx.Value(validationContextKey{}))
			checked = append(checked, proxy+"|"+host)
			return nil
		}), nil
	})
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	req, err := http.NewRequestWithContext(context.WithValue(t.Context(), validationContextKey{}, "request-context"), http.MethodGet, "https://upstream.example/a", nil)
	require.NoError(t, err)
	for _, proxy := range []string{"ss://encrypted-proxy", "http://http-proxy", "socks5://socks-proxy"} {
		transport, err := newValidatedTransport(base, proxy)
		require.NoError(t, err)
		for range 2 {
			response, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
		}
	}
	require.Equal(t, []string{"ss://encrypted-proxy|upstream.example", "http://http-proxy|upstream.example", "socks5://socks-proxy|upstream.example"}, checked)
}

func TestValidatedTransportCancellationStopsBusinessRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var businessCalls int
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		businessCalls++
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	transport := newTestValidatedTransport(t, base, func(ctx context.Context, _ string) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://upstream.example/a", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, businessCalls)
}

func TestBuildClientBindsValidationToSelectedProxy(t *testing.T) {
	for _, proxy := range []string{"ss://aes-128-gcm:test@127.0.0.1:12345", "http://127.0.0.1:12345", "socks5://127.0.0.1:12345"} {
		t.Run(proxy, func(t *testing.T) {
			var selected string
			setTestValidatorFactory(t, func(proxy string) (resolvedIPValidator, error) {
				selected = proxy
				return validatorFunc(func(context.Context, string) error { return nil }), nil
			})
			client, err := buildClient(Options{ProxyURL: proxy, ValidateResolvedIP: true, OpenAINative: true})
			require.NoError(t, err)
			defer client.CloseIdleConnections()
			require.Equal(t, proxy, selected)
		})
	}
}

func TestValidatedTransportChecksRedirectTargets(t *testing.T) {
	var checked, sent []string
	failure := errors.New("redirect resolves to private IP")
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sent = append(sent, req.URL.Hostname())
		return &http.Response{StatusCode: http.StatusFound, Body: http.NoBody, Header: http.Header{"Location": {"https://private.example/next"}}, Request: req}, nil
	})
	transport := newTestValidatedTransport(t, base, func(ctx context.Context, host string) error {
		checked = append(checked, host)
		if host == "private.example" {
			return failure
		}
		return nil
	})
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://public.example/first", nil)
	require.NoError(t, err)
	response, err := client.Do(req)
	require.ErrorIs(t, err, failure)
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, []string{"public.example", "private.example"}, checked)
	require.Equal(t, []string{"public.example"}, sent)
}
