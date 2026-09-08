package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const codexAuxiliaryStickyTestSession = "history-notes-shared-session"

var codexAuxiliaryStickyTestBody = []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"history-notes-shared-session\",\"thread_id\":\"history-notes-shared-thread\"}"},"context":{"session_id":"history-notes-shared-session"}}`)

type codexAuxiliaryStickyTestCacheKey struct {
	groupID int64
	key     string
}

// Preserve the real cache's group isolation while reusing its unrelated stubs.
type codexAuxiliaryStickyTestCache struct {
	stubGatewayCache
	bindings map[codexAuxiliaryStickyTestCacheKey]int64
	reads    []context.Context
}

func (c *codexAuxiliaryStickyTestCache) GetSessionAccountID(ctx context.Context, groupID int64, key string) (int64, error) {
	c.reads = append(c.reads, ctx)
	return c.bindings[codexAuxiliaryStickyTestCacheKey{groupID: groupID, key: key}], nil
}

func (c *codexAuxiliaryStickyTestCache) SetSessionAccountID(_ context.Context, groupID int64, key string, accountID int64, _ time.Duration) error {
	c.bindings[codexAuxiliaryStickyTestCacheKey{groupID: groupID, key: key}] = accountID
	return nil
}

func newCodexAuxiliaryStickyTestService(t *testing.T, upstream func(*http.Request, int64) (*http.Response, error)) (*OpenAIGatewayService, *APIKey, *codexAuxiliaryStickyTestCache) {
	t.Helper()
	groupID := int64(17)
	accounts := []Account{
		{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-first-token", "chatgpt_account_id": "test-first-account"}},
		{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-second-token", "chatgpt_account_id": "test-second-account"}},
	}
	cache := &codexAuxiliaryStickyTestCache{bindings: make(map[codexAuxiliaryStickyTestCacheKey]int64)}
	svc := &OpenAIGatewayService{
		accountRepo: codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{groupID: accounts}},
		cache:       cache,
		cfg: &config.Config{
			JWT: config.JWTConfig{Secret: "history-notes-sticky-test-secret"},
			Gateway: config.GatewayConfig{OpenAIWS: config.GatewayOpenAIWSConfig{
				SessionHashReadOldFallback: true,
				SessionHashDualWriteOld:    true,
			}},
		},
		httpUpstream: &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
			return upstream(req, accountID)
		}},
	}
	return svc, &APIKey{ID: 31, UserID: 7, GroupID: &groupID}, cache
}

func newCodexAuxiliaryStickyTestContext(ctx context.Context, apiKey *APIKey, path string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/backend-api/codex"+path, bytes.NewReader(codexAuxiliaryStickyTestBody)).WithContext(ctx)
	c.Set("api_key", apiKey)
	return c
}

func codexAuxiliaryStickyTestResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"items":[]}`))}
}

func TestForwardCodexHistoryNotesReusesResponsesStickyBinding(t *testing.T) {
	var calls []int64
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(req *http.Request, accountID int64) (*http.Response, error) {
		calls = append(calls, accountID)
		require.Equal(t, "/backend-api/codex/alpha/history/v2/list_windows", req.URL.Path)
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	modelContext := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/responses")
	SetOpenAIOAuthIdentityCapture(modelContext, CaptureOpenAIOAuthIdentity(modelContext, codexAuxiliaryStickyTestBody, ""))
	modelHash := svc.GenerateSessionHashForOpenAIOAuthIdentity(modelContext, codexAuxiliaryStickyTestBody, "")
	require.NoError(t, svc.BindStickySession(modelContext.Request.Context(), apiKey.GroupID, modelHash, 22))
	// A binding for another group must not replace the current group's winner.
	require.NoError(t, cache.SetSessionAccountID(context.Background(), *apiKey.GroupID+1, svc.openAISessionCacheKey(modelHash), 11, time.Minute))

	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []int64{22}, calls)
	accountID, err := svc.getStickySessionAccountID(c.Request.Context(), apiKey.GroupID, modelHash)
	require.NoError(t, err)
	require.Equal(t, int64(22), accountID)
}

func TestForwardCodexHistoryNotesFallbackRebindsResponsesSticky(t *testing.T) {
	for _, failure := range []string{"connection", "503"} {
		t.Run(failure, func(t *testing.T) {
			var calls []int64
			svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, accountID int64) (*http.Response, error) {
				calls = append(calls, accountID)
				if accountID == 22 {
					if failure == "connection" {
						return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("test connection refused")}
					}
					return codexAuxiliaryStickyTestResponse(http.StatusServiceUnavailable), nil
				}
				return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
			})
			hash, legacyHash := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
			require.NoError(t, svc.BindStickySession(withOpenAILegacySessionHash(context.Background(), legacyHash), apiKey.GroupID, hash, 22))
			c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
			resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, []int64{22, 11}, calls)
			accountID, err := svc.getStickySessionAccountID(c.Request.Context(), apiKey.GroupID, hash)
			require.NoError(t, err)
			require.Equal(t, int64(11), accountID)
			require.Equal(t, int64(11), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + legacyHash}])

			// Use a fresh auxiliary service instance to ensure Redis, rather than
			// the process-local fallback, supplies the next Notes binding.
			nextSvc, _, _ := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, accountID int64) (*http.Response, error) {
				calls = append(calls, accountID)
				return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
			})
			nextSvc.cache = cache
			next := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/notes/v2/list_files_by_prefix")
			resp, err = nextSvc.ForwardCodexHistoryNotes(next.Request.Context(), next, apiKey, "/alpha/notes/v2/list_files_by_prefix", codexAuxiliaryStickyTestBody)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, []int64{22, 11, 11}, calls)
		})
	}
}

func TestForwardCodexHistoryNotesPermissionFailurePreservesResponsesSticky(t *testing.T) {
	var calls []int64
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, accountID int64) (*http.Response, error) {
		calls = append(calls, accountID)
		return codexAuxiliaryStickyTestResponse(http.StatusForbidden), nil
	})
	hash, legacyHash := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
	require.NoError(t, svc.BindStickySession(withOpenAILegacySessionHash(context.Background(), legacyHash), apiKey.GroupID, hash, 22))
	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []int64{22}, calls)
	accountID, err := svc.getStickySessionAccountID(c.Request.Context(), apiKey.GroupID, hash)
	require.NoError(t, err)
	require.Equal(t, int64(22), accountID)
	require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + legacyHash}])
}

func TestForwardCodexHistoryNotesReadsLegacyBindingAndDualWrites(t *testing.T) {
	type callerContextKey struct{}
	callerCtx, cancel := context.WithTimeout(context.WithValue(context.Background(), callerContextKey{}, "caller-context"), time.Minute)
	defer cancel()
	var calls []int64
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(req *http.Request, accountID int64) (*http.Response, error) {
		calls = append(calls, accountID)
		require.Equal(t, "caller-context", req.Context().Value(callerContextKey{}))
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	hash, legacyHash := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
	cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + legacyHash}] = 22
	// The former auxiliary implementation wrote this erroneous unprefixed key.
	cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, hash}] = 11
	c := newCodexAuxiliaryStickyTestContext(callerCtx, apiKey, "/alpha/history/v2/list_windows")
	// This context is captured before GenerateSessionHash updates c.Request.
	resp, err := svc.ForwardCodexHistoryNotes(callerCtx, c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []int64{22}, calls)
	require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + hash}])
	require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + legacyHash}])
	require.Equal(t, int64(11), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, hash}])
	require.NotEmpty(t, cache.reads)
	for _, readCtx := range cache.reads {
		require.Equal(t, legacyHash, openAILegacySessionHashFromContext(readCtx))
		require.Equal(t, "caller-context", readCtx.Value(callerContextKey{}))
		wantDeadline, _ := callerCtx.Deadline()
		gotDeadline, ok := readCtx.Deadline()
		require.True(t, ok)
		require.Equal(t, wantDeadline, gotDeadline)
	}
}

func TestForwardCodexHistoryNotesIgnoresFormerUnprefixedBinding(t *testing.T) {
	var calls []int64
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, accountID int64) (*http.Response, error) {
		calls = append(calls, accountID)
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	hash, _ := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
	cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, hash}] = 22
	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []int64{11}, calls)
	require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, hash}])
	accountID, err := svc.getStickySessionAccountID(c.Request.Context(), apiKey.GroupID, hash)
	require.NoError(t, err)
	require.Equal(t, int64(11), accountID)
}
