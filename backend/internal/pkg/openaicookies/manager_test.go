package openaicookies

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testScope = Scope{OwnerAccountID: 1, OSFamily: "windows", AuthorizationGeneration: "grant-one"}

func localCookieClient(t *testing.T, manager *Manager, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}} // Local test certificate only.
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: manager.Wrap(transport)}
}

func doCookieRequest(t *testing.T, client *http.Client, ctx context.Context, target string, headers http.Header) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	require.NoError(t, err)
	request.Header = headers.Clone()
	response, err := client.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func capturedRequest(t *testing.T, client *http.Client, scope Scope, bundle Bundle, path string) *Attempt {
	t.Helper()
	ctx, attempt := WithAttempt(WithBundle(WithScope(context.Background(), scope), bundle))
	doCookieRequest(t, client, ctx, "https://chatgpt.com"+path, http.Header{"Cookie": {"caller=excluded"}})
	return attempt
}

func testBundle(t *testing.T, now time.Time, cookies ...*http.Cookie) Bundle {
	t.Helper()
	result := Bundle{ExpiresAt: now.Add(BundleLifetime)}
	target, _ := url.Parse("https://chatgpt.com/backend-api/codex/responses")
	for _, cookie := range cookies {
		entry, deleted, ok := normalize(target, cookie, now)
		require.True(t, ok)
		require.False(t, deleted)
		if entry.ExpiresAt.IsZero() || entry.ExpiresAt.After(result.ExpiresAt) {
			entry.ExpiresAt = result.ExpiresAt
		}
		if entry.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = entry.ExpiresAt
		}
		result.Entries = append(result.Entries, entry)
	}
	require.True(t, result.ValidAt(now))
	return result
}

func TestManagerFrozenBundleCanReplayAcrossOSAndNewProcesses(t *testing.T) {
	manager := NewManager()
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	var sent []string
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Cookie"))
		if r.URL.Path == "/collect" {
			w.Header().Add("Set-Cookie", "__oailb=route; Path=/; Max-Age=3600; Secure")
			w.Header().Add("Set-Cookie", "__cflb=session; Path=/; Secure")
			w.Header().Add("Set-Cookie", "login_session=excluded; Path=/")
		}
	})
	attempt := capturedRequest(t, client, testScope, Bundle{}, "/collect")
	require.Empty(t, sent[0])
	bundle, err := attempt.Snapshot(now.Add(BundleLifetime))
	require.NoError(t, err)
	require.Len(t, bundle.Entries, 2)
	for _, entry := range bundle.Entries {
		require.Equal(t, now.Add(BundleLifetime), entry.ExpiresAt)
	}
	for _, os := range []string{"windows", "macos", "linux"} {
		scope := testScope
		scope.OSFamily = os
		next := NewManager()
		next.now = manager.now
		replay := localCookieClient(t, next, func(w http.ResponseWriter, r *http.Request) {
			require.True(t, strings.Contains(r.Header.Get("Cookie"), "__oailb=") && strings.Contains(r.Header.Get("Cookie"), "__cflb="))
			require.False(t, strings.Contains(r.Header.Get("Cookie"), "caller="))
		})
		captured := capturedRequest(t, replay, scope, bundle, "/business")
		copy, err := captured.Snapshot(now.Add(BundleLifetime))
		require.NoError(t, err)
		require.ElementsMatch(t, bundle.Entries, copy.Entries)
	}
	// The same manager cannot supply cookies to an unrelated model/attempt. Only
	// the explicitly selected bundle can supply a base, including an empty base.
	empty := capturedRequest(t, client, testScope, Bundle{}, "/fresh-other-model")
	blank, err := empty.Snapshot(now.Add(BundleLifetime))
	require.NoError(t, err)
	require.Empty(t, blank.Entries)
	require.True(t, blank.ValidAt(now))
	require.Empty(t, sent[1])
}

func TestManagerDisabledAndBoundAuxiliaryCompletelyBypass(t *testing.T) {
	now := time.Now()
	bundle := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "saved", Path: "/"})
	contexts := []context.Context{context.Background(), WithScope(context.Background(), testScope), Bypass(WithBundle(WithScope(context.Background(), testScope), bundle))}
	for _, ctx := range contexts {
		ctx, attempt := WithAttempt(ctx)
		original := http.Header{"Cookie": {"already-filtered=original"}, "X-Codex-Turn-State": {"original-ticket"}}
		client := localCookieClient(t, NewManager(), func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, original.Get("Cookie"), r.Header.Get("Cookie"))
			require.Equal(t, original.Get("X-Codex-Turn-State"), r.Header.Get("X-Codex-Turn-State"))
			w.Header().Add("Set-Cookie", "__oailb=ignored; Path=/; Max-Age=3600")
		})
		doCookieRequest(t, client, ctx, "https://chatgpt.com/auxiliary", original)
		_, err := attempt.Snapshot(now.Add(BundleLifetime))
		require.ErrorIs(t, err, ErrNoSnapshot)
	}
}

func TestManagerSendGuardRestoresOriginalRequestBeforeBypass(t *testing.T) {
	ctx := WithBundle(WithScope(context.Background(), testScope), Bundle{})
	ctx = WithSendGuard(ctx, func(request *http.Request) bool {
		request.Header.Set("X-Codex-Turn-State", "original-ticket")
		return false
	})
	ctx, attempt := WithAttempt(ctx)
	client := localCookieClient(t, NewManager(), func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "original-ticket", r.Header.Get("X-Codex-Turn-State"))
		require.Equal(t, "original=value", r.Header.Get("Cookie"))
		require.Equal(t, "Bearer synthetic", r.Header.Get("Authorization"))
		w.Header().Add("Set-Cookie", "__oailb=ignored; Path=/")
	})
	doCookieRequest(t, client, ctx, "https://chatgpt.com/", http.Header{"Cookie": {"original=value"}, "X-Codex-Turn-State": {"injected-ticket"}, "Authorization": {"Bearer synthetic"}})
	_, err := attempt.Snapshot(time.Now().Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
}

func TestManagerExpiredBundleRejectsEveryCookieAndCachedTicket(t *testing.T) {
	now := time.Now()
	bundle := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "expires-first", Path: "/", MaxAge: 1}, &http.Cookie{Name: "__cflb", Value: "still-live", Path: "/", MaxAge: 60})
	manager := NewManager()
	manager.now = func() time.Time { return now.Add(2 * time.Second) }
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.Header.Get("X-Codex-Turn-State"))
		w.Header().Add("Set-Cookie", "__oailb=ignored; Path=/")
	})
	ctx, attempt := WithAttempt(WithBundle(WithScope(context.Background(), testScope), bundle))
	doCookieRequest(t, client, ctx, "https://chatgpt.com/", http.Header{"Cookie": {"caller=blocked"}, "X-Codex-Turn-State": {"cached-ticket"}})
	_, err := attempt.Snapshot(now.Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
}

func TestManagerEphemeralAuthorizationRedirectsStayIsolated(t *testing.T) {
	manager := NewManager()
	var cookies []string
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		cookies = append(cookies, r.Header.Get("Cookie"))
		if r.URL.Path == "/start" {
			w.Header().Add("Set-Cookie", "__cflb=flow; Path=/; Secure")
			http.Redirect(w, r, "/finish", http.StatusFound)
		}
	})
	ctx := WithScope(Bypass(context.Background()), Scope{EphemeralID: "first-flow"})
	doCookieRequest(t, client, ctx, "https://chatgpt.com/start", nil)
	require.Equal(t, []string{"", "__cflb=flow"}, cookies)
	doCookieRequest(t, client, WithScope(context.Background(), Scope{EphemeralID: "second-flow"}), "https://chatgpt.com/finish", nil)
	require.Empty(t, cookies[2])
	manager.ClearEphemeral("first-flow")
	doCookieRequest(t, client, ctx, "https://chatgpt.com/finish", nil)
	require.Empty(t, cookies[3])
}

func TestCookieDomainPathAndHostPolicy(t *testing.T) {
	now := time.Now()
	origin, _ := url.Parse("https://chatgpt.com/backend-api/codex/plugins/list")
	var entries []Entry
	for _, cookie := range []*http.Cookie{{Name: "__oailb", Value: "host", Path: "/"}, {Name: "__oailb", Value: "domain", Domain: ".chatgpt.com", Path: "/backend-api"}, {Name: "__cflb", Value: "default"}} {
		entry, _, ok := normalize(origin, cookie, now)
		require.True(t, ok)
		entries = append(entries, entry)
	}
	for _, check := range []struct {
		url   string
		count int
	}{{"https://chatgpt.com/backend-api/codex/responses", 2}, {"https://chatgpt.com/backend-api/codex/plugins/item", 3}, {"https://sub.chatgpt.com/backend-api/codex/responses", 1}, {"https://chatgpt.com/backend-api-other", 1}} {
		r, _ := http.NewRequest(http.MethodGet, check.url, nil)
		require.Equal(t, check.count, apply(r, entries, now).SentCount)
	}
	for _, target := range []string{"http://chatgpt.com/", "https://api.openai.com/", "https://chatgpt.com.evil.example/", "https://sub.chat.openai.com/"} {
		u, _ := url.Parse(target)
		require.False(t, AllowedURL(u))
	}
	for _, cookie := range []*http.Cookie{{Name: "session", Value: "blocked"}, {Name: "__oailb", Value: "blocked", Domain: "evil.example"}, {Name: "__oailb", Value: "blocked", Domain: "com"}} {
		_, _, accepted := normalize(origin, cookie, now)
		require.False(t, accepted)
	}
}
