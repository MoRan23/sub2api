package openaicookies

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerWebsocketNeverReceivesBundleCookieHandling(t *testing.T) {
	ctx, attempt := WithAttempt(WithBundle(WithScope(context.Background(), testScope), Bundle{}))
	client := localCookieClient(t, NewManager(), func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "preexisting=unchanged", request.Header.Get("Cookie"))
		w.Header().Add("Set-Cookie", "__oailb=not-captured; Path=/")
	})
	doCookieRequest(t, client, ctx, "https://chatgpt.com/", http.Header{"Upgrade": {"h2c, WebSocket"}, "Cookie": {"preexisting=unchanged"}})
	_, err := attempt.Snapshot(time.Now().Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
}

func TestManagerForeignRedirectDoesNotReceiveFrozenCookies(t *testing.T) {
	now := time.Now()
	bundle := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "route", Path: "/"})
	client := localCookieClient(t, NewManager(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			require.Equal(t, "__oailb=route", r.Header.Get("Cookie"))
			http.Redirect(w, r, "https://unrelated.example/final", http.StatusFound)
			return
		}
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.Header.Get("X-Codex-Turn-State"))
	})
	ctx, attempt := WithAttempt(WithBundle(WithScope(context.Background(), testScope), bundle))
	doCookieRequest(t, client, ctx, "https://chatgpt.com/start", http.Header{"X-Codex-Turn-State": {"cached"}})
	_, err := attempt.Snapshot(now.Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
}

func TestManagerCreationOrderSurvivesResponseReplacement(t *testing.T) {
	now := time.Now()
	base := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "first", Path: "/"})
	base.Entries[0].CreatedAt = now.Add(-time.Minute)
	manager := NewManager()
	manager.now = func() time.Time { return now }
	client := localCookieClient(t, manager, func(w http.ResponseWriter, request *http.Request) {
		w.Header().Add("Set-Cookie", "__cflb=second; Path=/")
		w.Header().Add("Set-Cookie", "__oailb=replacement; Path=/")
	})
	attempt := capturedRequest(t, client, testScope, base, "/")
	bundle, err := attempt.Snapshot(now.Add(BundleLifetime))
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	require.NoError(t, err)
	apply(request, bundle.Entries, now)
	require.Equal(t, "__oailb=replacement; __cflb=second", request.Header.Get("Cookie"))
}

func TestManagerFreshCollectorBundleClearsInheritedGuardAndFallback(t *testing.T) {
	ctx := WithSendGuard(WithScope(context.Background(), testScope), func(*http.Request) bool {
		t.Fatal("independent collector inherited business guard")
		return false
	})
	ctx = WithFallback(ctx, func(*http.Request) {
		t.Fatal("independent collector inherited business fallback")
	})
	ctx, attempt := WithAttempt(WithBundle(ctx, Bundle{}))
	client := localCookieClient(t, NewManager(), func(w http.ResponseWriter, request *http.Request) {
		require.Empty(t, request.Header.Get("Cookie"))
	})
	doCookieRequest(t, client, ctx, "https://chatgpt.com/", http.Header{"Cookie": {"previous=excluded"}})
	bundle, err := attempt.Snapshot(time.Now().Add(BundleLifetime))
	require.NoError(t, err)
	require.True(t, bundle.ValidAt(time.Now()))
	// Force a rejection as well; a successful send would never call a fallback.
	doCookieRequest(t, client, ctx, "https://unrelated.example/", nil)
}
