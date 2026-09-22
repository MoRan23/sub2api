package openaicookies

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerStrictRejectionNeverSendsOrRestoresPreparedRequest(t *testing.T) {
	for _, failure := range []string{"guard", "scope", "manager", "host", "expired_after_guard", "malformed", "disabled", "missing_bundle", "closed_attempt"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Now()
			manager := NewManager()
			manager.now = func() time.Time { return now }
			bundle := testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "private-cookie", Path: "/"})
			if failure == "malformed" {
				bundle.Entries[0].Key = "malformed"
			}
			scope := testScope
			if failure == "scope" {
				scope = Scope{}
			}
			ctx := WithBundle(WithScope(context.Background(), scope), bundle)
			if failure == "missing_bundle" {
				ctx = WithScope(context.Background(), scope)
			}
			if failure == "disabled" {
				ctx = Bypass(ctx)
			}
			cause := errors.New("private-ticket https://secret.example/private")
			ctx = WithRejectedSendError(ctx, cause)
			ctx = WithSendGuard(ctx, func(request *http.Request) bool {
				request.Header.Set("X-Guard-Clone", "yes")
				if failure == "expired_after_guard" {
					now = bundle.ExpiresAt
				}
				return failure != "guard"
			})
			fallbacks := 0
			ctx = WithFallback(ctx, func(*http.Request) { fallbacks++ })
			var diagnostics []Diagnostic
			ctx = WithObserver(ctx, func(value Diagnostic) { diagnostics = append(diagnostics, value) })
			sendObservations := 0
			ctx = WithSendObserver(ctx, func(*http.Request) { sendObservations++ })
			ctx, attempt := WithAttempt(ctx)
			if failure == "closed_attempt" {
				attempt.Discard()
			}
			target := "https://chatgpt.com/backend-api/codex/responses"
			if failure == "host" {
				target = "https://unrelated.example/private"
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("prepared-body"))
			require.NoError(t, err)
			request.Header.Set("X-Codex-Turn-State", "private-ticket")
			request.Header.Set("Cookie", "__oailb=original-private-cookie")
			originalHeaders := request.Header.Clone()
			calls := 0
			if failure == "manager" {
				manager = nil
			}
			response, err := manager.Wrap(cookieRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK}, nil
			})).RoundTrip(request)
			require.Nil(t, response)
			require.ErrorIs(t, err, ErrBundleSendRejected)
			require.ErrorIs(t, err, cause)
			require.EqualError(t, err, "cookie_bundle_send_rejected")
			require.Zero(t, calls)
			require.Zero(t, sendObservations)
			require.Zero(t, fallbacks, "strict failure requires a complete baseline rebuild by the caller")
			require.Equal(t, originalHeaders, request.Header)
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Equal(t, "prepared-body", string(body))
			require.Len(t, diagnostics, 1)
			require.Equal(t, "not_sent", diagnostics[0].SendState)
			require.False(t, diagnostics[0].Sent)
			require.Zero(t, diagnostics[0].SentCount)
			safe, err := json.Marshal(diagnostics)
			require.NoError(t, err)
			require.NotContains(t, string(safe), "private")
			_, err = attempt.Snapshot(now.Add(BundleLifetime))
			if failure == "closed_attempt" {
				require.ErrorIs(t, err, ErrAttemptClosed)
			} else {
				require.ErrorIs(t, err, ErrNoSnapshot)
			}
		})
	}
}

func TestManagerStrictAcceptedSendUsesFinalCloneAndCapturesCandidate(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved_bundle", true: "fresh_collector"}[fresh], func(t *testing.T) {
			now := time.Now()
			manager := NewManager()
			manager.now = func() time.Time { return now }
			bundle := Bundle{}
			if !fresh {
				bundle = testBundle(t, now, &http.Cookie{Name: "__oailb", Value: "saved-private", Path: "/"})
			}
			ctx := WithRejectedSendError(WithBundle(WithScope(context.Background(), testScope), bundle), ErrBundleSendRejected)
			ctx = WithSendGuard(ctx, func(request *http.Request) bool {
				request.Header.Set("Authorization", "Bearer validated")
				return true
			})
			var diagnostics []Diagnostic
			ctx = WithObserver(ctx, func(value Diagnostic) { diagnostics = append(diagnostics, value) })
			ctx, attempt := WithAttempt(ctx)
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/", nil)
			require.NoError(t, err)
			request.Header.Set("Cookie", "__cflb=caller-private")
			calls := 0
			_, err = manager.Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "Bearer validated", outbound.Header.Get("Authorization"))
				require.NotContains(t, outbound.Header.Get("Cookie"), "caller-private")
				if fresh {
					require.Empty(t, outbound.Header.Get("Cookie"))
				} else {
					require.Equal(t, "__oailb=saved-private", outbound.Header.Get("Cookie"))
				}
				require.Equal(t, "sent", diagnostics[len(diagnostics)-1].SendState)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__cflb=new-private; Path=/"}}}, nil
			})).RoundTrip(request)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, "__cflb=caller-private", request.Header.Get("Cookie"))
			require.Empty(t, request.Header.Get("Authorization"))
			require.Equal(t, !fresh, diagnostics[0].Sent)
			candidate, err := attempt.Snapshot(now.Add(BundleLifetime))
			require.NoError(t, err)
			require.True(t, candidate.ValidAt(now))
			require.Equal(t, "sent", attempt.Diagnostic().SendState)
		})
	}
}

func TestManagerStrictRejectionInvalidatesPreviousCandidate(t *testing.T) {
	now := time.Now()
	manager := NewManager()
	manager.now = func() time.Time { return now }
	ctx := WithRejectedSendError(WithBundle(WithScope(context.Background(), testScope), Bundle{}), ErrBundleSendRejected)
	allowed := true
	ctx = WithSendGuard(ctx, func(*http.Request) bool { return allowed })
	ctx, attempt := WithAttempt(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
	require.NoError(t, err)
	calls := 0
	transport := manager.Wrap(cookieRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK}, nil
	}))
	_, err = transport.RoundTrip(request)
	require.NoError(t, err)
	_, err = attempt.Snapshot(now.Add(BundleLifetime))
	require.NoError(t, err)
	allowed = false
	_, err = transport.RoundTrip(request)
	require.ErrorIs(t, err, ErrBundleSendRejected)
	require.Equal(t, 1, calls)
	_, err = attempt.Snapshot(now.Add(BundleLifetime))
	require.ErrorIs(t, err, ErrNoSnapshot)
}

func TestManagerStrictPolicyDoesNotLeakIntoNewOperation(t *testing.T) {
	for _, reset := range []string{"bundle", "ephemeral", "explicit_nil"} {
		t.Run(reset, func(t *testing.T) {
			ctx := WithRejectedSendError(WithBundle(WithScope(context.Background(), testScope), Bundle{}), ErrBundleSendRejected)
			ctx = WithSendGuard(ctx, func(*http.Request) bool { return false })
			switch reset {
			case "bundle":
				ctx = WithBundle(ctx, Bundle{})
			case "ephemeral":
				ctx = WithScope(ctx, Scope{EphemeralID: "new-flow"})
			case "explicit_nil":
				ctx = WithRejectedSendError(ctx, nil)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
			require.NoError(t, err)
			calls := 0
			_, err = NewManager().Wrap(cookieRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK}, nil
			})).RoundTrip(request)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestManagerBypassDiagnosticReadsOnlyFinalAllowedCookieNames(t *testing.T) {
	for _, value := range []string{"", "__oailb=private-value; login_session=private-login; __cflb=private-other"} {
		ctx := Bypass(WithBundle(WithScope(context.Background(), testScope), Bundle{}))
		var diagnostic Diagnostic
		ctx = WithObserver(ctx, func(value Diagnostic) { diagnostic = value })
		ctx, attempt := WithAttempt(ctx)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
		require.NoError(t, err)
		request.Header.Set("Cookie", value)
		_, err = NewManager().Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
			require.Same(t, request, outbound)
			require.Equal(t, value, outbound.Header.Get("Cookie"))
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=ignored; Path=/"}}}, nil
		})).RoundTrip(request)
		require.NoError(t, err)
		require.Equal(t, "sent", diagnostic.SendState)
		if value == "" {
			require.False(t, diagnostic.Sent)
			require.Equal(t, "none", diagnostic.Source)
			require.Zero(t, diagnostic.SentCount)
		} else {
			require.True(t, diagnostic.Sent)
			require.Equal(t, "client", diagnostic.Source)
			require.Equal(t, 2, diagnostic.SentCount)
			require.Equal(t, []string{"__cflb", "__oailb"}, diagnostic.Names)
		}
		safe, err := json.Marshal(diagnostic)
		require.NoError(t, err)
		require.NotContains(t, string(safe), "private")
		require.NotContains(t, string(safe), "login_session")
		_, err = attempt.Snapshot(time.Now().Add(BundleLifetime))
		require.ErrorIs(t, err, ErrNoSnapshot)
	}
}

func TestManagerStrictPolicyDoesNotHandleNativeWebSocket(t *testing.T) {
	ctx := WithRejectedSendError(WithBundle(context.Background(), Bundle{}), ErrBundleSendRejected)
	ctx = WithSendGuard(ctx, func(*http.Request) bool { t.Fatal("native WS invoked HTTP guard"); return false })
	ctx = WithObserver(ctx, func(Diagnostic) { t.Fatal("native WS created cookie diagnostics") })
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
	require.NoError(t, err)
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Cookie", "original=unchanged")
	_, err = NewManager().Wrap(cookieRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
		require.Same(t, request, outbound)
		require.Equal(t, "original=unchanged", outbound.Header.Get("Cookie"))
		return &http.Response{StatusCode: http.StatusSwitchingProtocols}, nil
	})).RoundTrip(request)
	require.NoError(t, err)
}
