package openaicookies

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cookieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cookieRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAttemptFrozenExpiryDeletionAndSnapshotIsolation(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager()
	manager.now = func() time.Time { return now }
	base := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "old", Path: "/", MaxAge: 10}, &http.Cookie{Name: "__cflb", Value: "delete", Path: "/"})
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/update" {
			w.Header().Add("Set-Cookie", "__cflb=gone; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:01 GMT; Path=/")
			w.Header().Add("Set-Cookie", "__cf_bm=new-session; Path=/; Secure")
		}
	})
	staged := capturedRequest(t, client, testScope, base, "/update")
	copy, err := staged.Snapshot(now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, copy.Entries, 2)
	require.Equal(t, now.Add(10*time.Second), copy.ExpiresAt)
	for _, entry := range copy.Entries {
		if entry.Name == "__cf_bm" {
			require.Equal(t, now.Add(BundleLifetime), entry.ExpiresAt)
		}
		require.NotEqual(t, "__cflb", entry.Name)
	}
	copy.Entries[0].Value = "changed-copy"
	independent, err := staged.Snapshot(now.Add(BundleLifetime))
	require.NoError(t, err)
	require.NotEqual(t, copy.Entries[0].Value, independent.Entries[0].Value)
	metadata := staged.Diagnostic()
	require.NotEmpty(t, metadata.Cookies)
	*metadata.Cookies[0].ExpiresAt = time.Time{}
	require.False(t, staged.Diagnostic().Cookies[0].ExpiresAt.IsZero())
	now = now.Add(11 * time.Second)
	_, err = staged.Snapshot(now.Add(BundleLifetime))
	require.ErrorIs(t, err, ErrBundleExpired, "a later target deadline never renews inherited cookies")
}

func TestAttemptCaptureFailureCannotBecomeValidEmptyBundle(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		err  error
	}{{"rejected", 403, nil}, {"server error", 500, nil}, {"redirect", 302, nil}, {"transport failure", 200, errors.New("synthetic failure")}} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager()
			ctx, attempt := WithAttempt(WithBundle(WithScope(context.Background(), testScope), Bundle{}))
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
			require.NoError(t, err)
			transport := manager.Wrap(cookieRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.code, Header: http.Header{"Set-Cookie": {"__oailb=discard; Path=/"}}}, test.err
			}))
			_, err = transport.RoundTrip(request)
			require.ErrorIs(t, err, test.err)
			_, err = attempt.Snapshot(time.Now().Add(BundleLifetime))
			require.ErrorIs(t, err, ErrNoSnapshot)
			attempt.MarkPublished()
			require.NotEqual(t, "cookie_bundle_saved", attempt.Diagnostic().Reason)
		})
	}
}

func TestAttemptPublicationMarkerSurvivesDiscard(t *testing.T) {
	client := localCookieClient(t, NewManager(), func(http.ResponseWriter, *http.Request) {})
	accepted := capturedRequest(t, client, testScope, Bundle{}, "/")
	result, err := accepted.Snapshot(time.Now().Add(BundleLifetime))
	require.NoError(t, err)
	require.True(t, result.ValidAt(time.Now()))
	require.Empty(t, result.Entries)
	accepted.MarkPublished()
	accepted.MarkPublished()
	accepted.Discard()
	require.Equal(t, "cookie_bundle_saved", accepted.Diagnostic().Reason)
	_, err = accepted.Snapshot(time.Now().Add(BundleLifetime))
	require.ErrorIs(t, err, ErrAttemptClosed)
	rejected := capturedRequest(t, client, testScope, Bundle{}, "/")
	rejected.Discard()
	rejected.MarkPublished()
	require.Equal(t, "cookie_target_rejected", rejected.Diagnostic().Reason)
}

func TestAttemptScopeChangesCannotReuseCapturedResponse(t *testing.T) {
	client := localCookieClient(t, NewManager(), func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Set-Cookie", "__oailb=one; Path=/") })
	ctx, attempt := WithAttempt(WithBundle(WithScope(context.Background(), testScope), Bundle{}))
	doCookieRequest(t, client, ctx, "https://chatgpt.com/", nil)
	other := testScope
	other.OwnerAccountID++
	doCookieRequest(t, client, WithScope(ctx, other), "https://chatgpt.com/", nil)
	_, err := attempt.Snapshot(time.Now().Add(BundleLifetime))
	require.ErrorIs(t, err, ErrAttemptClosed)
}

func TestAttemptNilHelpersAndBundleValidation(t *testing.T) {
	ctx, attempt := WithAttempt(nil)
	require.NotNil(t, ctx)
	_, err := attempt.Snapshot(time.Now().Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
	var absent *Attempt
	_, err = absent.Snapshot(time.Now().Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
	absent.Discard()
	absent.MarkPublished()
	require.True(t, (Bundle{}).Fresh())
	require.False(t, (Bundle{}).ValidAt(time.Now()))
	require.NotNil(t, WithBundle(nil, Bundle{}))
	require.NotNil(t, Bypass(nil))
	require.NotNil(t, WithSendGuard(nil, nil))
	require.NotNil(t, WithFallback(nil, nil))
}
