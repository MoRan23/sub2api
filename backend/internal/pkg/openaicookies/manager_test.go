package openaicookies

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cookieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cookieRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestManagerTemporaryFlowsAreIsolatedAndCleared(t *testing.T) {
	manager := NewManager()
	send := func(flow, expected string, cookies []string) {
		ctx := WithScope(context.Background(), Scope{EphemeralID: flow})
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/auth", nil)
		require.NoError(t, err)
		request.Header.Set("Cookie", "caller=excluded")
		_, err = manager.Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
			require.Equal(t, expected, outbound.Header.Get("Cookie"))
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": cookies}}, nil
		})).RoundTrip(request)
		require.NoError(t, err)
		require.Equal(t, "caller=excluded", request.Header.Get("Cookie"))
	}
	send("flow-a", "", []string{"__oailb=one; Secure; Path=/", "private=ignored; Secure; Path=/"})
	send("flow-a", "__oailb=one", nil)
	send("flow-b", "", []string{"__oailb=two; Secure; Path=/"})
	send("flow-b", "__oailb=two", nil)
	manager.ClearEphemeral("flow-a")
	send("flow-a", "", nil)
	send("flow-b", "__oailb=two", nil)
}

func TestManagerBoundRequestsBypassCookieHandling(t *testing.T) {
	manager := NewManager()
	for _, scope := range []Scope{{}, {OwnerAccountID: 31, OSFamily: "windows", AuthorizationGeneration: "grant"}} {
		for range 2 {
			request, err := http.NewRequestWithContext(WithScope(context.Background(), scope), http.MethodGet, "https://chatgpt.com/responses", nil)
			require.NoError(t, err)
			request.Header.Set("Cookie", "original=preserved")
			request.Header.Set("X-Codex-Turn-State", "opaque-original")
			require.False(t, EnabledForRequest(request))
			_, err = manager.Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				require.Same(t, request, outbound)
				require.Equal(t, "original=preserved", outbound.Header.Get("Cookie"))
				require.Equal(t, "opaque-original", outbound.Header.Get("X-Codex-Turn-State"))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=must-not-be-learned; Secure; Path=/"}}}, nil
			})).RoundTrip(request)
			require.NoError(t, err)
		}
	}
	require.Empty(t, manager.memory)
}

func TestManagerTemporaryCookieScopeExpiryAndDeletion(t *testing.T) {
	now := time.Now()
	manager := NewManager()
	manager.now = func() time.Time { return now }
	ctx := WithScope(context.Background(), Scope{EphemeralID: "flow"})
	send := func(target, expected string, cookies []string, headers http.Header) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		require.NoError(t, err)
		request.Header = headers.Clone()
		_, err = manager.Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
			require.Equal(t, expected, outbound.Header.Get("Cookie"))
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": cookies}}, nil
		})).RoundTrip(request)
		require.NoError(t, err)
	}
	send("https://chatgpt.com/auth/start", "", []string{"__oailb=route; Secure; Path=/auth; Max-Age=10"}, nil)
	send("https://chatgpt.com/auth/next", "__oailb=route", nil, nil)
	send("https://sub.chatgpt.com/auth/next", "", nil, nil)
	send("https://chatgpt.com/other", "", nil, nil)
	send("https://foreign.invalid/auth", "", nil, http.Header{"Cookie": {"__oailb=route"}})
	send("http://chatgpt.com/auth", "", nil, http.Header{"Cookie": {"__oailb=route"}})
	send("https://chatgpt.com/auth", "ws=original", nil, http.Header{"Upgrade": {"websocket"}, "Cookie": {"ws=original"}})
	now = now.Add(11 * time.Second)
	send("https://chatgpt.com/auth", "", []string{"__oailb=replaced; Secure; Path=/auth"}, nil)
	send("https://chatgpt.com/auth", "__oailb=replaced", []string{"__oailb=deleted; Secure; Path=/auth; Max-Age=0"}, nil)
	send("https://chatgpt.com/auth", "", nil, nil)
}
