package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIHTTPProtocolFreezesModelCapabilitiesWithoutRerouting(t *testing.T) {
	account := mergeHTTPAccount()
	account.Credentials["model_mapping"] = map[string]any{"public": "gpt-6-sol"}
	gateway := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	namespace := openAICodexModelCapabilitiesNamespace(account)
	gateway.codexModelCapabilities.observeManifest(namespace, []byte(`{"models":[{"slug":"gpt-6-sol","use_responses_lite":true}]}`), time.Now())
	body := []byte(`{"model":"public","input":"hi"}`)
	gateway.prepareOpenAIHTTPProtocol(c, account, body, "responses", "")
	first, ok := frozenOpenAIHTTPModelCapabilities(c, account, "gpt-6-sol")
	require.True(t, ok)
	require.True(t, first.UseResponsesLite)
	require.Equal(t, account.Proxy.URL(), FreezeOpenAIOutboundRoute(c, account).ProxyURL)

	// A directory update cannot change the protocol of an already prepared retry.
	gateway.codexModelCapabilities.observeManifest(namespace, []byte(`{"models":[{"slug":"gpt-6-sol","use_responses_lite":false}]}`), time.Now())
	gateway.prepareOpenAIHTTPProtocol(c, account, body, "responses", "")
	retry, ok := frozenOpenAIHTTPModelCapabilities(c, account, "gpt-6-sol")
	require.True(t, ok)
	require.Equal(t, first, retry)
	_, wrongModel := frozenOpenAIHTTPModelCapabilities(c, account, "gpt-5.5")
	require.False(t, wrongModel)

	other := *account
	other.ID = account.ID + 1
	_, wrongAccount := frozenOpenAIHTTPModelCapabilities(c, &other, "gpt-6-sol")
	require.False(t, wrongAccount)
	gateway.prepareOpenAIHTTPBridgeProtocol(c, account, []byte(`{"model":"gpt-6-sol"}`), true)
	next, ok := frozenOpenAIHTTPModelCapabilities(c, account, "gpt-6-sol")
	require.True(t, ok)
	require.False(t, next.UseResponsesLite, "a new HTTP bridge turn can observe the updated model directory")
}

func TestOpenAIResponseEvidenceHandshakeOnlyClaimedOnce(t *testing.T) {
	conn := &openAIWSConn{handshakeHeaders: http.Header{"Safety-Buffering": {"true"}}}
	firstLease := &openAIWSConnLease{conn: conn}
	first := firstLease.ClaimResponseEvidenceHeaders()
	require.Equal(t, "true", first.Get("Safety-Buffering"))
	first.Set("Safety-Buffering", "false")
	require.Equal(t, "true", conn.handshakeHeaders.Get("Safety-Buffering"), "observation must not mutate physical headers")
	require.Nil(t, (&openAIWSConnLease{conn: conn}).ClaimResponseEvidenceHeaders(), "pool reuse does not assign old handshake evidence to a new turn")
}
