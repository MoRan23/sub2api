package service

import (
	"context"
	"crypto/sha256"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGuardianRoutingPinScopeExpiryAndEarlyBinding(t *testing.T) {
	now := time.Now()
	r := newOpenAIGuardianPinRegistry()
	request := &openAIGuardianRoutingRequest{scope: sha256.Sum256([]byte("user/group/account/owner/generation/os/proxy")), ttl: time.Hour}
	const target = "wss://chatgpt.com/backend-api/codex/responses"
	id := r.capture(request, target, http.Header{"Set-Cookie": {"__oailb=parent; Path=/backend-api/; Max-Age=10; Secure", "__cflb=edge; Path=/; Secure"}}, now)
	require.NotEmpty(t, id)
	r.observe(request.scope, id, []byte(`{"type":"response.created","response":{"id":"resp_parent"}}`), now)
	require.Equal(t, id, r.lookup(request.scope, "resp_parent", now))
	other := sha256.Sum256([]byte("different-owner-or-user"))
	require.Empty(t, r.lookup(other, "resp_parent", now))
	reviewer := *request
	reviewer.parentPinID = id
	headers := http.Header{"Cookie": {"unrelated=ok; __oailb=stale; __cflb=stale"}}
	r.apply(&reviewer, target, headers, now)
	require.Contains(t, headers.Get("Cookie"), "__oailb=parent")
	require.Contains(t, headers.Get("Cookie"), "__cflb=edge")
	require.Contains(t, headers.Get("Cookie"), "unrelated=ok")
	require.NotContains(t, headers.Get("Cookie"), "stale")
	headers = make(http.Header)
	r.apply(&reviewer, target, headers, now.Add(11*time.Second))
	require.Equal(t, "__cflb=edge", headers.Get("Cookie"))
	require.Equal(t, id, r.lookup(request.scope, "resp_parent", now.Add(11*time.Second)), "cookie expiration does not change physical connection compatibility")
	for _, wrongTarget := range []string{"https://chatgpt.com/backend-api/other", "https://other.chatgpt.com/backend-api/codex/responses", "http://chatgpt.com/backend-api/codex/responses", "https://example.com/backend-api/codex/responses"} {
		headers = make(http.Header)
		r.apply(&reviewer, wrongTarget, headers, now)
		require.Empty(t, headers.Get("Cookie"), wrongTarget)
	}
	// Lookups never extend the original response retention.
	require.Empty(t, r.lookup(request.scope, "resp_parent", now.Add(time.Hour)))
}

func TestOpenAIGuardianRoutingPinRejectsInvalidCookies(t *testing.T) {
	const target = "wss://chatgpt.com/backend-api/codex/responses"
	for name, cookies := range map[string][]string{
		"duplicate":      {"__oailb=a; Path=/", "__oailb=b; Path=/"},
		"oversize":       {"__oailb=" + strings.Repeat("x", 8192)},
		"foreign domain": {"__oailb=a; Domain=example.com; Path=/"},
		"wrong path":     {"__oailb=a; Path=/elsewhere"},
		"expired":        {"__oailb=a; Max-Age=0; Path=/"},
		"unrelated":      {"session=secret; Path=/"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newOpenAIGuardianPinRegistry()
			require.Empty(t, r.capture(&openAIGuardianRoutingRequest{ttl: time.Hour}, target, http.Header{"Set-Cookie": cookies}, time.Now()))
		})
	}
}

func TestOpenAIGuardianRoutingPinBoundedAndNoRefresh(t *testing.T) {
	r := newOpenAIGuardianPinRegistry()
	r.maxEntries = 2
	r.maxBytes = 2048
	now := time.Now()
	req := &openAIGuardianRoutingRequest{ttl: time.Minute}
	const target = "https://chatgpt.com/backend-api/codex/responses"
	var ids []string
	for _, responseID := range []string{"one", "two", "three"} {
		id := r.capture(req, target, http.Header{"Set-Cookie": {"__oailb=one; Path=/"}}, now)
		ids = append(ids, id)
		r.bind(req.scope, id, responseID, now)
		require.LessOrEqual(t, len(r.pins)+len(r.responses), r.maxEntries)
	}
	require.Equal(t, 2, len(r.pins)+len(r.responses))
	require.LessOrEqual(t, r.bytes, r.maxBytes)
	require.Empty(t, r.lookup(req.scope, "one", now))
	require.Equal(t, ids[2], r.lookup(req.scope, "three", now.Add(59*time.Second)))
	r.bind(req.scope, ids[2], "three", now.Add(59*time.Second))
	require.Empty(t, r.lookup(req.scope, "three", now.Add(time.Minute)))
}

func TestOpenAIWSParentPinCompatibility(t *testing.T) {
	req := openAIWSAcquireRequest{Headers: http.Header{"Originator": {"thread-agent"}}, Routing: &openAIGuardianRoutingRequest{parentPinID: "one"}}
	before := openAIWSAcquireCompatibility(req)
	req.Routing = &openAIGuardianRoutingRequest{parentPinID: "two"}
	require.NotEqual(t, before, openAIWSAcquireCompatibility(req))
	req.Routing = &openAIGuardianRoutingRequest{parentPinID: "one"}
	req.Headers.Set("Originator", "other-thread-agent")
	require.NotEqual(t, before, openAIWSAcquireCompatibility(req))
}

func TestCodexSafePairPreservesIndependentThreadOriginator(t *testing.T) {
	const ua = "codex-tui/0.170.0 (Ubuntu 24.04; x86_64) xterm (codex-tui; 0.170.0)"
	h := http.Header{"User-Agent": {ua}, "Originator": {"codex-review-worker"}, "Version": {"0.170.0"}}
	pairCodexIdentityHeadersWithFallback(h, codexOutboundIdentity{originator: "codex-tui", userAgent: ua, version: "0.170.0"})
	require.Equal(t, "codex-review-worker", h.Get("Originator"))
	require.Equal(t, ua, h.Get("User-Agent"))
	for _, values := range [][]string{{"a", "b"}, {"bad\r\nheader"}, {" a "}, {strings.Repeat("x", 257)}} {
		h["Originator"] = values
		pairCodexIdentityHeadersWithFallback(h, codexOutboundIdentity{originator: "codex-tui", userAgent: ua, version: "0.170.0"})
		require.Equal(t, "codex-tui", h.Get("Originator"))
	}
}

func TestOpenAIGuardianRoutingPreparationOwnershipAndReviewer(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	group := int64(70013)
	key := &APIKey{ID: 30012, UserID: 70011, GroupID: &group}
	c.Set("api_key", key)
	account := &Account{ID: 70012, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	plan := OpenAIOAuthIdentityPlan{CredentialOwnerNamespace: "account:70012", AuthorizationGeneration: "first", CredentialOS: "linux"}
	var svc *OpenAIGatewayService
	const target = "wss://chatgpt.com/backend-api/codex/responses"
	request := svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-one", nil, nil)
	require.NotNil(t, request)
	now := time.Now()
	id := openAIGuardianPins.capture(request, target, http.Header{"Set-Cookie": {"__oailb=parent; Path=/"}}, now)
	openAIGuardianPins.bind(request.scope, id, "resp_scoped", now)
	body := []byte(`{"client_metadata":{"parent_response_id":"resp_scoped"}}`)
	for _, marker := range []string{"", "classifier", "unknown", "reviewer"} {
		headers := http.Header{"X-Codex-Guardian": {marker}}
		got := svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-one", headers, body)
		if marker == "reviewer" {
			require.Equal(t, id, got.parentPinID)
		} else {
			require.Empty(t, got.parentPinID)
		}
	}
	key.ID++ // Owner scope is the user, not an arbitrary API key identity.
	require.Equal(t, request.scope, svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-one", nil, nil).scope)
	key.UserID++
	require.NotEqual(t, request.scope, svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-one", nil, nil).scope)
	key.UserID--
	for _, changed := range []OpenAIOAuthIdentityPlan{
		{CredentialOwnerNamespace: "account:70013", AuthorizationGeneration: "first", CredentialOS: "linux"},
		{CredentialOwnerNamespace: "account:70012", AuthorizationGeneration: "second", CredentialOS: "linux"},
		{CredentialOwnerNamespace: "account:70012", AuthorizationGeneration: "first", CredentialOS: "windows"},
	} {
		require.NotEqual(t, request.scope, svc.prepareOpenAIGuardianRouting(c, account, changed, target, "proxy-one", nil, nil).scope)
	}
	require.NotEqual(t, request.scope, svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-two", nil, nil).scope)
	group++
	require.NotEqual(t, request.scope, svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-one", nil, nil).scope)
	group--
	account.ID++
	require.NotEqual(t, request.scope, svc.prepareOpenAIGuardianRouting(c, account, plan, target, "proxy-one", nil, nil).scope)
}

func TestOpenAIGuardianRoutingRedirectDropsOnlyRoutingCookies(t *testing.T) {
	client := openAIGuardianRoutingRedirectGuard(&http.Client{}, "wss://chatgpt.com/backend-api/codex/responses")
	for _, target := range []string{"https://chatgpt.com/other", "https://other.chatgpt.com/backend-api/codex/responses", "http://chatgpt.com/backend-api/codex/responses"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Cookie", "__oailb=secret; untouched=ok; __cflb=secret")
		require.NoError(t, client.CheckRedirect(req, nil))
		require.Equal(t, "untouched=ok", req.Header.Get("Cookie"))
	}
}

func TestOpenAIThreadOriginatorCaptureAndNextFrame(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Originator", "my-reviewer")
	capture := CaptureOpenAIOAuthIdentity(c, []byte(`{"input":[]}`), "")
	require.Equal(t, "my-reviewer", capture.ThreadOriginator)
	plan := OpenAIOAuthIdentityPlan{Capture: capture}
	next := captureOpenAIWSFrameIdentity([]byte(`{"type":"response.create","client_metadata":{"session_id":"new-session"}}`), &plan)
	require.Equal(t, "my-reviewer", next.ThreadOriginator)
}

func TestOpenAIGuardianPinPhysicalHandshakeAndCreatedEvent(t *testing.T) {
	observedCookies := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		observedCookies <- req.Header.Get("Cookie")
		w.Header().Add("Set-Cookie", "__oailb=physical-parent; Path=/backend-api/; Secure; Max-Age=60")
		conn, err := coderws.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = conn.Write(req.Context(), coderws.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_live_parent"}}`))
		_, _, _ = conn.Read(req.Context())
	}))
	defer server.Close()
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	// This transport goes only to the local TLS fixture while preserving the
	// official endpoint hostname for production scope validation.
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client.Transport = transport
	defer transport.CloseIdleConnections()
	const target = "wss://chatgpt.com/backend-api/codex/responses"
	scope := newOpenAIWSTransportScope(70011, target, "")
	dialer := &coderOpenAIWSClientDialer{proxyClients: map[string]*openAIWSProxyClientEntry{scope.cacheKey(): {client: client}}}
	routing := &openAIGuardianRoutingRequest{scope: sha256.Sum256([]byte(t.Name())), ttl: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = withOpenAIGuardianRouting(withOpenAIWSTransportAccount(ctx, 70011), routing)
	parent, _, responseHeaders, err := dialer.Dial(ctx, target, make(http.Header), "")
	require.NoError(t, err)
	defer parent.Close()
	require.Empty(t, <-observedCookies)
	require.Empty(t, responseHeaders.Get("Set-Cookie"), "pooled evidence does not retain routing secrets")
	_, err = parent.ReadMessage(ctx)
	require.NoError(t, err)
	// No response.completed has been sent; the reviewer can already colocate.
	id := openAIGuardianPins.lookup(routing.scope, "resp_live_parent", time.Now())
	require.NotEmpty(t, id)
	reviewRouting := *routing
	reviewRouting.parentPinID = id
	reviewer, _, _, err := dialer.Dial(withOpenAIGuardianRouting(ctx, &reviewRouting), target, make(http.Header), "")
	require.NoError(t, err)
	defer reviewer.Close()
	require.Equal(t, "__oailb=physical-parent", <-observedCookies)
}

func BenchmarkOpenAIGuardianRoutingDeltaObservation(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			payload := []byte(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("x", size) + `"}`)
			r := newOpenAIGuardianPinRegistry()
			var scope [32]byte
			now := time.Now()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r.observe(scope, "pin", payload, now)
			}
		})
	}
}
