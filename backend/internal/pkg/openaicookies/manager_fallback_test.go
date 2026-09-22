package openaicookies

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerBundleRejectionInvokesFallbackBeforeSend(t *testing.T) {
	for _, name := range []string{"guard", "invalid_scope", "host", "expired_after_guard", "disabled"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			manager := NewManager()
			manager.now = func() time.Time { return now }
			bundle := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "frozen", Path: "/"})
			scope := testScope
			if name == "invalid_scope" {
				scope = Scope{}
			}
			ctx := WithBundle(WithScope(context.Background(), scope), bundle)
			guards, fallbacks := 0, 0
			ctx = WithSendGuard(ctx, func(*http.Request) bool {
				guards++
				if name == "expired_after_guard" {
					now = bundle.ExpiresAt
				}
				return name != "guard"
			})
			ctx = WithFallback(ctx, func(request *http.Request) {
				fallbacks++
				request.Header.Set("X-Codex-Turn-State", "original-ticket")
				request.Body = io.NopCloser(strings.NewReader("original-body"))
			})
			if name == "disabled" {
				ctx = Bypass(ctx)
			}
			ctx, attempt := WithAttempt(ctx)
			target := "https://chatgpt.com/"
			if name == "host" {
				target = "https://unrelated.example/"
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("prepared-body"))
			require.NoError(t, err)
			request.Header.Set("X-Codex-Turn-State", "prepared-ticket")
			request.Header.Set("Cookie", "filtered-original=preserved")
			request.Header.Set("Authorization", "Bearer normalized-after-preparation")
			transport := manager.Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				body, readErr := io.ReadAll(outbound.Body)
				require.NoError(t, readErr)
				if name == "disabled" {
					require.Same(t, request, outbound)
					require.Equal(t, "prepared-ticket", outbound.Header.Get("X-Codex-Turn-State"))
					require.Equal(t, "prepared-body", string(body))
				} else {
					require.Equal(t, "original-ticket", outbound.Header.Get("X-Codex-Turn-State"))
					require.Equal(t, "original-body", string(body))
				}
				require.Equal(t, "filtered-original=preserved", outbound.Header.Get("Cookie"))
				require.Equal(t, "Bearer normalized-after-preparation", outbound.Header.Get("Authorization"))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=must-not-capture; Path=/"}}}, nil
			}))
			_, err = transport.RoundTrip(request)
			require.NoError(t, err)
			if name == "disabled" {
				require.Zero(t, guards)
				require.Zero(t, fallbacks)
			} else {
				require.Equal(t, 1, guards)
				require.Equal(t, 1, fallbacks)
			}
			_, err = attempt.Snapshot(now.Add(BundleLifetime))
			require.ErrorIs(t, err, ErrNoSnapshot)
		})
	}
}

func TestManagerEphemeralScopeClearsInheritedBundleFallback(t *testing.T) {
	ctx := WithBundle(WithScope(context.Background(), testScope), Bundle{})
	ctx = WithFallback(ctx, func(*http.Request) { t.Fatal("OAuth flow inherited a business fallback") })
	ctx = WithSendGuard(ctx, func(*http.Request) bool {
		t.Fatal("OAuth flow inherited a business guard")
		return false
	})
	ctx = WithScope(ctx, Scope{EphemeralID: "independent-oauth-flow"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://unrelated.example/", strings.NewReader("oauth-body"))
	require.NoError(t, err)
	_, err = NewManager().Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
		require.Empty(t, outbound.Header.Get("X-Codex-Turn-State"))
		body, readErr := io.ReadAll(outbound.Body)
		require.NoError(t, readErr)
		require.Equal(t, "oauth-body", string(body))
		return &http.Response{StatusCode: http.StatusOK}, nil
	})).RoundTrip(request)
	require.NoError(t, err)
}
