package repository

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamResidencyRedirectGuardIsRequestScoped(t *testing.T) {
	upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
	base := &http.Client{}
	plain, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://upstream.example/responses", nil)
	require.NoError(t, err)
	// Even an inherited header does not classify a non-OpenAI business request.
	plain.Header.Set(openai.CodexResidencyHeaderName, "eu")
	require.Same(t, base, upstream.httpClientForUpstreamRequest(base, plain))
	require.False(t, openai.IsCodexResidencyRequest(plain))
	marked := openai.MarkCodexResidencyRequest(plain)
	require.True(t, openai.IsCodexResidencyRequest(marked))
	client := upstream.httpClientForUpstreamRequest(base, marked)
	require.NotSame(t, base, client)
	require.Nil(t, base.CheckRedirect)
	hop, _ := http.NewRequestWithContext(marked.Context(), http.MethodGet, "https://other.example/redirect", nil)
	hop.Header.Set(openai.CodexResidencyHeaderName, "eu")
	require.NoError(t, client.CheckRedirect(hop, []*http.Request{marked}))
	require.Empty(t, hop.Header.Get(openai.CodexResidencyHeaderName))
	require.Equal(t, "eu", plain.Header.Get(openai.CodexResidencyHeaderName))
}

func TestHTTPUpstreamResidencyRedirectGuardKeepsTransportConstraints(t *testing.T) {
	upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
	base := &http.Client{}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://upstream.example/responses", nil)
	request = openai.MarkCodexResidencyRequest(request)
	disabled := request.WithContext(service.WithHTTPUpstreamRedirectsDisabled(request.Context()))
	client := upstream.httpClientForUpstreamRequest(base, disabled)
	require.ErrorIs(t, client.CheckRedirect(disabled, []*http.Request{disabled}), http.ErrUseLastResponse)
	publicOnly := request.WithContext(service.WithHTTPUpstreamPublicHostsOnly(request.Context()))
	client = upstream.httpClientForUpstreamRequest(base, publicOnly)
	privateHop, _ := http.NewRequestWithContext(publicOnly.Context(), http.MethodGet, "http://127.0.0.1/redirect", nil)
	privateHop.Header.Set(openai.CodexResidencyHeaderName, "us")
	require.Error(t, client.CheckRedirect(privateHop, []*http.Request{publicOnly}))
	require.Empty(t, privateHop.Header.Get(openai.CodexResidencyHeaderName))
	require.Nil(t, base.CheckRedirect)
}
