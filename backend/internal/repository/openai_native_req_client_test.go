package repository

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeReqPolicyPreservesFinalUAAndRedirectDispatch(t *testing.T) {
	client := req.C().ImpersonateFirefox()
	originalUA := client.Headers.Get("User-Agent")
	require.Contains(t, originalUA, "Macintosh")
	jar := client.GetClient().Jar
	redirects := 0
	client.GetClient().CheckRedirect = func(request *http.Request, via []*http.Request) error {
		redirects++
		request.Header.Set("User-Agent", "codex-tui/0.154.0 (Windows 10.0.26100; x86_64)")
		return nil
	}
	var platforms []codexnative.Platform
	var observed []*http.Request
	dispatcher := codexnative.NewDispatcher(nil, func(platform codexnative.Platform) (http.RoundTripper, error) {
		return req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			platforms = append(platforms, platform)
			observed = append(observed, request.Clone(request.Context()))
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}
			if len(observed) == 1 {
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://other.test/complete")
				response.Header.Set("Set-Cookie", "state=kept; Path=/")
			}
			return response, nil
		}), nil
	})
	client.GetClient().Transport = dispatcher
	ctx := codexnative.WithScope(context.Background(), codexnative.Scope{AccountID: 7, AccountUserAgent: "codex-tui (Ubuntu 24.04.3)", Purpose: "privacy"})
	derived := openai.ReqClientWithRequestPolicy(client, ctx)
	require.Same(t, dispatcher, derived.GetClient().Transport)
	require.Nil(t, derived.GetClient().Jar)
	response, err := derived.R().SetContext(ctx).Get("https://origin.test/account")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, []codexnative.Platform{codexnative.MacOS, codexnative.Windows}, platforms)
	require.Equal(t, 1, redirects)
	require.Equal(t, originalUA, observed[0].Header.Get("User-Agent"), "keep Firefox application UA despite the Linux account hint")
	require.Equal(t, originalUA, client.Headers.Get("User-Agent"), "request cloning must not mutate common headers")
	require.Equal(t, "us", observed[0].Header.Get(openai.CodexResidencyHeaderName))
	require.Empty(t, observed[1].Header.Get(openai.CodexResidencyHeaderName), "cross-origin redirect guard remains active")
	origin, err := url.Parse("https://origin.test/account")
	require.NoError(t, err)
	require.Empty(t, jar.Cookies(origin), "OpenAI requests must not populate req's shared jar")
	for _, request := range observed {
		scope, ok := codexnative.ScopeFromContext(request.Context())
		require.True(t, ok)
		require.Equal(t, int64(7), scope.AccountID)
	}
}

func TestOpenAINativeReqFactorySeparatesCacheAndRecordsTiming(t *testing.T) {
	options := reqClientOptions{Timeout: 7 * time.Second}
	ordinary, err := getSharedReqClient(options)
	require.NoError(t, err)
	options.OpenAINativeHTTP = true
	native, err := getSharedReqClient(options)
	require.NoError(t, err)
	require.NotSame(t, ordinary, native)
	require.NotNil(t, ordinary.GetClient().Jar)
	require.Nil(t, native.GetClient().Jar)
	require.IsType(t, &codexnative.Dispatcher{}, native.GetClient().Transport)
	require.Equal(t, 7*time.Second, native.GetClient().Timeout)
	defer native.GetClient().CloseIdleConnections()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "OTel-Go/1.0", request.Header.Get("User-Agent"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	collector := servertiming.New(time.Now())
	ctx := servertiming.WithCollector(context.Background(), collector)
	ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 99, AccountUserAgent: "codex-tui (Ubuntu 24.04.3)", Purpose: "metrics"})
	response, err := openai.ReqClientWithRequestPolicy(native, ctx).R().SetContext(ctx).SetHeader("User-Agent", "OTel-Go/1.0").Get(server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Contains(t, collector.HeaderValue(time.Now(), "bypass"), "dep_http;dur=")
}

func TestOpenAINativeReqWrapperPreservesExplicitResponseDecompression(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "HTTP/1.1", request.Proto)
		require.Contains(t, request.Header.Get("User-Agent"), "Firefox/")
		require.Equal(t, "gzip", request.Header.Get("Accept-Encoding"))
		require.Equal(t, uint16(tls.VersionTLS12), request.TLS.Version)
		require.Empty(t, request.TLS.NegotiatedProtocol)
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		writer := gzip.NewWriter(w)
		_, err := writer.Write([]byte(`{"privacy":"preserved"}`))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
	}))
	defer server.Close()
	client := req.C().ImpersonateFirefox().EnableAutoDecompress()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client.GetTransport().TLSClientConfig.RootCAs = roots
	client.GetClient().Transport = newOpenAINativeReqDispatcher(client)
	defer client.GetClient().CloseIdleConnections()
	ctx := codexnative.WithScope(context.Background(), codexnative.Scope{AccountID: 33, Purpose: "privacy"})
	response, err := openai.ReqClientWithRequestPolicy(client, ctx).R().SetContext(ctx).SetHeader("Accept-Encoding", "gzip").Get(server.URL)
	require.NoError(t, err)
	require.Equal(t, `{"privacy":"preserved"}`, response.String())
	require.Empty(t, response.Header.Get("Content-Encoding"))
}
