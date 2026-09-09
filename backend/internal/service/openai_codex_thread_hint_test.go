package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForwardCodexHistoryNotesThreadHintFreshness(t *testing.T) {
	const path = "/alpha/notes/v2/thread_hint"
	for _, tc := range []struct {
		name      string
		prepare   string
		observe   bool
		wantNew   bool
		wantCalls int
	}{
		{name: "new_session_without_observation", wantNew: true, wantCalls: 1},
		{name: "new_session_with_observation", observe: true, wantNew: true, wantCalls: 1},
		{name: "existing_identity_without_sticky", prepare: "identity", wantCalls: 1},
		{name: "redis_sticky_without_identity", prepare: "redis", wantCalls: 1},
		{name: "local_sticky_without_identity", prepare: "local", wantCalls: 1},
		{name: "fallback_to_new_identity", prepare: "fallback", wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetProcessCodexIdentityStore(t)
			SetFingerprintObservationEnabled(false)
			SetFingerprintObservationEnabled(tc.observe)
			t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
			calls := 0
			svc, key, cache := newCodexAuxiliaryStickyTestService(t, func(req *http.Request, accountID int64) (*http.Response, error) {
				calls++
				require.Equal(t, "/backend-api/codex"+path, req.URL.Path)
				status := http.StatusNotFound
				if tc.prepare == "fallback" && accountID == 11 {
					status = http.StatusServiceUnavailable
				}
				resp := codexAuxiliaryStickyTestResponse(status)
				resp.Body = io.NopCloser(strings.NewReader(`{"detail":"Not found"}`))
				return resp, nil
			})
			if tc.prepare == "identity" {
				seed := newCodexAuxiliaryStickyTestContext(context.Background(), key, "/responses")
				capture, err := captureCodexAuxiliaryIdentity(codexAuxiliaryStickyTestBody)
				require.NoError(t, err)
				accounts, err := svc.listCodexAuxiliaryAccounts(context.Background(), key)
				require.NoError(t, err)
				_, err = svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), seed, accounts[0], capture, OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true}, nil)
				require.NoError(t, err)
			}
			if tc.prepare == "redis" || tc.prepare == "local" {
				seed := newCodexAuxiliaryStickyTestContext(context.Background(), key, path)
				capture, err := captureCodexAuxiliaryIdentity(codexAuxiliaryStickyTestBody)
				require.NoError(t, err)
				SetOpenAIOAuthIdentityCapture(seed, capture)
				hash := svc.GenerateSessionHashForOpenAIOAuthIdentity(seed, codexAuxiliaryStickyTestBody, capture.Logical.SessionKey)
				if tc.prepare == "redis" {
					require.NoError(t, svc.BindStickySession(seed.Request.Context(), key.GroupID, hash, 11))
				} else {
					svc.storeCodexAuxiliarySticky(hash, 11, *key.GroupID)
				}
			}
			c := newCodexAuxiliaryStickyTestContext(context.Background(), key, path)
			resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, key, path, codexAuxiliaryStickyTestBody)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, resp.StatusCode, "the service preserves the upstream response; normalization waits for handler delivery")
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.JSONEq(t, `{"detail":"Not found"}`, string(body))
			require.Equal(t, tc.wantNew, IsNewCodexThreadHintRequest(c))
			require.Equal(t, tc.wantCalls, calls)
			if tc.observe {
				require.Equal(t, http.StatusNotFound, codexContextObservationFromContext(c).UpstreamHTTPStatus)
			}
			if tc.wantNew {
				require.Empty(t, cache.bindings, "a missing hint must not bind Responses affinity")
				next := newCodexAuxiliaryStickyTestContext(context.Background(), key, path)
				repeated, err := svc.ForwardCodexHistoryNotes(next.Request.Context(), next, key, path, codexAuxiliaryStickyTestBody)
				require.NoError(t, err)
				require.NoError(t, repeated.Body.Close())
				require.False(t, IsNewCodexThreadHintRequest(next), "reused upstream identities are not new sessions")
			}
		})
	}
}

func TestForwardCodexHistoryNotesDoesNotMarkOtherOperationsAsThreadHint(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, key, _ := newCodexAuxiliaryStickyTestService(t, func(*http.Request, int64) (*http.Response, error) {
		return codexAuxiliaryStickyTestResponse(http.StatusNotFound), nil
	})
	const path = "/alpha/notes/v2/read_file"
	c := newCodexAuxiliaryStickyTestContext(context.Background(), key, path)
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, key, path, codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.False(t, IsNewCodexThreadHintRequest(c))
	require.False(t, IsNewCodexThreadHintRequest(nil))
}
