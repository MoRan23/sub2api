package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const codexAuxiliaryStickyTestSession = "history-notes-shared-session"

var codexAuxiliaryStickyTestBody = []byte(`{"context":{"session_id":"history-notes-shared-session","current_agent_name":"/root/worker"}}`)
var codexAuxiliaryResponsesTestBody = []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"history-notes-shared-session\",\"thread_id\":\"history-notes-shared-thread\"}"}}`)

type codexAuxiliaryStickyTestCacheKey struct {
	groupID int64
	key     string
}

// Preserve the real cache's group isolation while reusing its unrelated stubs.
type codexAuxiliaryStickyTestCache struct {
	stubGatewayCache
	bindings          map[codexAuxiliaryStickyTestCacheKey]int64
	reads             []context.Context
	auxiliaryBindings sync.Map
	bindingErr        error
	identityStoreOnce sync.Once
	identityStore     *openAICodexIdentityLocalStore
}

func (c *codexAuxiliaryStickyTestCache) downstreamIdentityStore() *openAICodexIdentityLocalStore {
	c.identityStoreOnce.Do(func() { c.identityStore = newOpenAICodexIdentityLocalStore() })
	return c.identityStore
}

func (c *codexAuxiliaryStickyTestCache) ResolveCodexDownstreamSession(ctx context.Context, request OpenAICodexDownstreamSessionRequest, ttl time.Duration) (OpenAICodexDownstreamSessionResolution, error) {
	return c.downstreamIdentityStore().ResolveCodexDownstreamSession(ctx, request, ttl)
}

func (c *codexAuxiliaryStickyTestCache) ResolveCodexDownstreamThread(ctx context.Context, request OpenAICodexDownstreamThreadRequest, ttl time.Duration) (OpenAICodexTurnIdentity, error) {
	return c.downstreamIdentityStore().ResolveCodexDownstreamThread(ctx, request, ttl)
}

func (c *codexAuxiliaryStickyTestCache) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	return processOpenAICodexWindowLocalStore.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, threadID, ttl)
}

func (c *codexAuxiliaryStickyTestCache) ResolveCodexAuxiliaryAccountBinding(_ context.Context, key string, eligible []int64, preferred int64) (CodexAuxiliaryAccountBinding, error) {
	if c.bindingErr != nil {
		return CodexAuxiliaryAccountBinding{}, c.bindingErr
	}
	return resolveLocalCodexAuxiliaryAccountBinding(&c.auxiliaryBindings, key, eligible, preferred)
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
	expiresAt := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	accounts := []Account{
		{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-first-token", "chatgpt_account_id": "test-first-account", "plan_type": "plus", "subscription_expires_at": expiresAt}},
		{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-second-token", "chatgpt_account_id": "test-second-account", "plan_type": "pro", "subscription_expires_at": expiresAt}},
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
	var serverSession string
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(req *http.Request, accountID int64) (*http.Response, error) {
		calls = append(calls, accountID)
		require.Equal(t, "/backend-api/codex/alpha/history/v2/list_windows", req.URL.Path)
		var sent map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&sent))
		require.Equal(t, map[string]any{"context": map[string]any{"session_id": serverSession, "current_agent_name": "/root/worker"}}, sent)
		replayBody, replayErr := req.GetBody()
		require.NoError(t, replayErr)
		var replayed map[string]any
		require.NoError(t, json.NewDecoder(replayBody).Decode(&replayed))
		require.NoError(t, replayBody.Close())
		require.Equal(t, sent, replayed, "transport replay must not restore client identity")
		require.NotEqual(t, codexAuxiliaryStickyTestSession, serverSession)
		require.Empty(t, req.Header.Get("x-codex-turn-metadata"))
		require.Empty(t, req.Header.Get("x-codex-window-id"))
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	modelContext := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/responses")
	SetOpenAIOAuthIdentityCapture(modelContext, CaptureOpenAIOAuthIdentity(modelContext, codexAuxiliaryResponsesTestBody, ""))
	modelHash := svc.GenerateSessionHashForOpenAIOAuthIdentity(modelContext, codexAuxiliaryResponsesTestBody, "")
	accounts, err := svc.listCodexAuxiliaryAccounts(context.Background(), apiKey)
	require.NoError(t, err)
	modelCapture, _ := OpenAIOAuthIdentityCaptureFromContext(modelContext)
	modelPlan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), modelContext, accounts[1], modelCapture, OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true}, nil)
	require.NoError(t, err)
	serverSession = modelPlan.WireProfile.SessionID
	require.NoError(t, svc.BindStickySession(modelContext.Request.Context(), apiKey.GroupID, modelHash, 22))
	// A binding for another group must not replace the current group's winner.
	require.NoError(t, cache.SetSessionAccountID(context.Background(), *apiKey.GroupID+1, svc.openAISessionCacheKey(modelHash), 11, time.Minute))

	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
	c.Request.Header.Set("x-codex-turn-metadata", `{"session_id":"spoofed-session"}`)
	c.Request.Header.Set("x-codex-window-id", "spoofed-window:9")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []int64{22}, calls)
	accountID, err := svc.getStickySessionAccountID(c.Request.Context(), apiKey.GroupID, modelHash)
	require.NoError(t, err)
	require.Equal(t, int64(22), accountID)
}

type codexAuxiliaryReaderFunc func([]byte) (int, error)

func (f codexAuxiliaryReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestForwardCodexHistoryNotesIncompleteResponsePreservesBinding(t *testing.T) {
	for _, failure := range []string{"eof", "timeout", "invalid_json", "oversize"} {
		t.Run(failure, func(t *testing.T) {
			var calls []int64
			svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
				calls = append(calls, id)
				resp := codexAuxiliaryStickyTestResponse(http.StatusOK)
				switch failure {
				case "eof":
					resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"items":`), codexAuxiliaryReaderFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF })))
				case "timeout":
					resp.Body = io.NopCloser(codexAuxiliaryReaderFunc(func([]byte) (int, error) { return 0, context.DeadlineExceeded }))
				case "invalid_json":
					resp.Body = io.NopCloser(strings.NewReader(`{"items":`))
				case "oversize":
					resp.ContentLength = codexAuxiliaryResponseLimit + 1
				}
				return resp, nil
			})
			hash, _ := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
			require.NoError(t, svc.BindStickySession(context.Background(), apiKey.GroupID, hash, 22))
			c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
			resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
			require.Error(t, err)
			require.Nil(t, resp)
			require.Equal(t, []int64{22}, calls)
			require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
			binding, bindErr := cache.ResolveCodexAuxiliaryAccountBinding(context.Background(), codexAuxiliaryAccountBindingKey(apiKey, codexAuxiliaryStickyTestSession), []int64{11, 22}, 11)
			require.NoError(t, bindErr)
			require.Equal(t, int64(22), binding.AccountID)
			require.True(t, binding.Reused)
		})
	}
}

func TestForwardCodexHistoryNotesBindsBeforeRequestAndBuffersCompleteJSON(t *testing.T) {
	var svc *OpenAIGatewayService
	var apiKey *APIKey
	var cache *codexAuxiliaryStickyTestCache
	var responseContext context.Context
	var calls []int64
	hash, _ := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
	svc, apiKey, cache = newCodexAuxiliaryStickyTestService(t, func(req *http.Request, id int64) (*http.Response, error) {
		calls = append(calls, id)
		binding, err := cache.ResolveCodexAuxiliaryAccountBinding(req.Context(), codexAuxiliaryAccountBindingKey(apiKey, codexAuxiliaryStickyTestSession), []int64{11, 22}, 11)
		require.NoError(t, err)
		require.True(t, binding.Reused, "the account must be bound before any upstream side effect")
		require.Equal(t, id, binding.AccountID)
		responseContext = req.Context()
		resp := codexAuxiliaryStickyTestResponse(http.StatusOK)
		reader := strings.NewReader(`{"encrypted_output":"opaque", "items":[]}`)
		resp.Body = io.NopCloser(codexAuxiliaryReaderFunc(func(p []byte) (int, error) {
			require.NoError(t, req.Context().Err(), "deadline remains active until response EOF")
			require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}], "reading a response must not change Responses affinity")
			return reader.Read(p)
		}))
		return resp, nil
	})
	require.NoError(t, svc.BindStickySession(context.Background(), apiKey.GroupID, hash, 22))
	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/notes/v2/read_file")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/notes/v2/read_file", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.Equal(t, []int64{22}, calls)
	require.ErrorIs(t, responseContext.Err(), context.Canceled)
	require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"encrypted_output":"opaque", "items":[]}`, string(got))
	require.NoError(t, resp.Body.Close())
}

func TestForwardCodexHistoryNotesErrorsNeverSwitchAccounts(t *testing.T) {
	for _, path := range []string{"/alpha/history/v2/list_windows", "/alpha/notes/v2/read_file", "/alpha/notes/v2/append_to_file", "/alpha/notes/v2/write_file"} {
		for _, failure := range []string{"dial", "timeout", "eof", "body_eof", "invalid_json", "429", "503", "cancelled", "request_error"} {
			t.Run(path+"/"+failure, func(t *testing.T) {
				var calls []int64
				svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
					calls = append(calls, id)
					if len(calls) > 1 {
						return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
					}
					switch failure {
					case "dial":
						return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
					case "timeout":
						return nil, context.DeadlineExceeded
					case "eof":
						return nil, io.EOF
					case "cancelled":
						return nil, context.Canceled
					case "request_error":
						return nil, errors.New("invalid request")
					case "503":
						return codexAuxiliaryStickyTestResponse(http.StatusServiceUnavailable), nil
					case "429":
						return codexAuxiliaryStickyTestResponse(http.StatusTooManyRequests), nil
					}
					resp := codexAuxiliaryStickyTestResponse(http.StatusOK)
					if failure == "body_eof" {
						resp.Body = io.NopCloser(codexAuxiliaryReaderFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }))
					} else {
						resp.Body = io.NopCloser(strings.NewReader(`invalid`))
					}
					return resp, nil
				})
				hash, _ := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
				require.NoError(t, svc.BindStickySession(context.Background(), apiKey.GroupID, hash, 22))
				c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, path)
				resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, path, codexAuxiliaryStickyTestBody)
				require.Equal(t, []int64{22}, calls)
				if failure == "503" || failure == "429" {
					require.NoError(t, err)
					if failure == "503" {
						require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
					} else {
						require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
					}
				} else {
					require.Error(t, err)
					require.Nil(t, resp)
				}
				if resp != nil {
					require.NoError(t, resp.Body.Close())
				}
				require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
				// A later Responses request can migrate independently after the error.
				require.NoError(t, svc.BindStickySession(context.Background(), apiKey.GroupID, hash, 11))
				next := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, path)
				resp, err = svc.ForwardCodexHistoryNotes(next.Request.Context(), next, apiKey, path, codexAuxiliaryStickyTestBody)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, []int64{22, 22}, calls, "the next auxiliary request must retain the original account after any upstream error")
				require.Equal(t, int64(11), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
			})
		}
	}
}

func TestForwardCodexHistoryNotesSessionMappingIsolatesAPIKeys(t *testing.T) {
	var sessions []string
	svc, apiKey, _ := newCodexAuxiliaryStickyTestService(t, func(req *http.Request, _ int64) (*http.Response, error) {
		var sent struct {
			Context struct {
				SessionID string `json:"session_id"`
			} `json:"context"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&sent))
		sessions = append(sessions, sent.Context.SessionID)
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	second := *apiKey
	second.ID++
	for _, key := range []*APIKey{apiKey, &second, apiKey} {
		c := newCodexAuxiliaryStickyTestContext(context.Background(), key, "/alpha/history/v2/list_windows")
		resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, key, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.NotEmpty(t, sessions[0])
	require.NotEqual(t, sessions[0], sessions[1])
	require.Equal(t, sessions[0], sessions[2])
}

func TestForwardCodexHistoryNotesBindingIsolatesGroupKeyAndLogicalSession(t *testing.T) {
	var calls []int64
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
		calls = append(calls, id)
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	otherKey, otherGroup := *apiKey, *apiKey
	otherKey.ID++
	groupID := *apiKey.GroupID + 1
	otherGroup.GroupID = &groupID
	repo := svc.accountRepo.(codexModelsVisibilityAccountRepo)
	repo.byGroup[groupID] = repo.byGroup[*apiKey.GroupID]
	cases := []struct {
		key       *APIKey
		session   string
		accountID int64
	}{
		{apiKey, codexAuxiliaryStickyTestSession, 22},
		{&otherKey, codexAuxiliaryStickyTestSession, 11},
		{&otherGroup, codexAuxiliaryStickyTestSession, 11},
		{apiKey, "independent-logical-session", 11},
	}
	for _, tc := range cases {
		_, err := cache.ResolveCodexAuxiliaryAccountBinding(context.Background(), codexAuxiliaryAccountBindingKey(tc.key, tc.session), []int64{11, 22}, tc.accountID)
		require.NoError(t, err)
	}
	for _, tc := range append(cases, cases[0]) {
		const path = "/alpha/history/v2/list_windows"
		body := bytes.ReplaceAll(codexAuxiliaryStickyTestBody, []byte(codexAuxiliaryStickyTestSession), []byte(tc.session))
		c := newCodexAuxiliaryStickyTestContext(context.Background(), tc.key, path)
		resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, tc.key, path, body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.Equal(t, []int64{22, 11, 11, 11, 22}, calls)
	require.Empty(t, cache.bindings, "independent auxiliary bindings must not leak into shared Responses affinity")
}

func TestForwardCodexHistoryNotesBindingStoreFailureDoesNotCreateLocalFallback(t *testing.T) {
	var calls []int64
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
		calls = append(calls, id)
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	})
	cache.bindingErr = errors.New("binding cache unavailable")
	const path = "/alpha/notes/v2/append_to_file"
	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, path)
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, path, codexAuxiliaryStickyTestBody)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Empty(t, calls, "a cache failure cannot authorize a different account for a write")
	localEntries := 0
	svc.codexAuxiliarySticky.Range(func(_, _ any) bool { localEntries++; return true })
	require.Zero(t, localEntries)
}

func TestForwardCodexHistoryNotesReadSemanticFailuresDoNotFallback(t *testing.T) {
	for _, failure := range []string{"cancelled", "bad_request", "tls", "invalid_json", "missing_context"} {
		t.Run(failure, func(t *testing.T) {
			var calls []int64
			svc, apiKey, _ := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
				calls = append(calls, id)
				switch failure {
				case "cancelled":
					return nil, context.Canceled
				case "bad_request":
					return nil, errors.New("request configuration invalid")
				case "tls":
					return nil, &url.Error{Op: "Post", URL: "https://upstream.invalid", Err: errors.New("certificate verification failed")}
				default:
					resp := codexAuxiliaryStickyTestResponse(http.StatusOK)
					resp.Body = io.NopCloser(strings.NewReader("not json"))
					return resp, nil
				}
			})
			c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
			body := codexAuxiliaryStickyTestBody
			if failure == "missing_context" {
				body = []byte(`{"session_id":"root-only"}`)
			}
			resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", body)
			require.Error(t, err)
			require.Nil(t, resp)
			if failure == "missing_context" {
				require.ErrorIs(t, err, ErrCodexHistoryNotesInvalidContext)
				require.Empty(t, calls)
			} else {
				require.Equal(t, []int64{11}, calls)
			}
		})
	}
}

func TestBufferCodexAuxiliaryResponseBoundsUnknownLength(t *testing.T) {
	resp := codexAuxiliaryStickyTestResponse(http.StatusOK)
	resp.ContentLength = -1
	remaining := codexAuxiliaryResponseLimit + 4096
	var read int
	resp.Body = io.NopCloser(codexAuxiliaryReaderFunc(func(p []byte) (int, error) {
		if remaining == 0 {
			return 0, io.EOF
		}
		n := min(len(p), remaining)
		clear(p[:n])
		remaining -= n
		read += n
		return n, nil
	}))
	require.ErrorIs(t, bufferCodexAuxiliaryResponse(resp), errCodexAuxiliaryResponseTooLarge)
	require.Equal(t, codexAuxiliaryResponseLimit+1, read)
}

func TestForwardCodexHistoryNotesBindingSurvivesResponsesMigrationAndExpiry(t *testing.T) {
	var calls []int64
	upstream := func(_ *http.Request, accountID int64) (*http.Response, error) {
		calls = append(calls, accountID)
		return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
	}
	svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, upstream)
	hash, legacyHash := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
	ctx := withOpenAILegacySessionHash(context.Background(), legacyHash)
	require.NoError(t, svc.BindStickySession(ctx, apiKey.GroupID, hash, 22))
	for i, path := range []string{"/alpha/history/v2/list_windows", "/alpha/notes/v2/list_files_by_prefix", "/alpha/history/v2/search_contents"} {
		if i == 1 {
			// Responses migration cannot overwrite the independent auxiliary binding.
			require.NoError(t, svc.BindStickySession(ctx, apiKey.GroupID, hash, 11))
		} else if i == 2 {
			// Model-session TTL expiry and a service restart cannot forget it either.
			clear(cache.bindings)
			svc, _, _ = newCodexAuxiliaryStickyTestService(t, upstream)
			svc.cache = cache
		}
		c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, path)
		resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, path, codexAuxiliaryStickyTestBody)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		if i == 1 {
			require.Equal(t, int64(11), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + hash}])
			require.Equal(t, int64(11), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + legacyHash}])
		}
	}
	require.Equal(t, []int64{22, 22, 22}, calls)
	require.Empty(t, cache.bindings, "auxiliary calls must not recreate expired Responses bindings")
}

func TestForwardCodexHistoryNotesRebindsOnlyWhenAccountLosesEligibility(t *testing.T) {
	for _, reason := range []string{"subscription_expired", "unschedulable", "disabled", "removed"} {
		t.Run(reason, func(t *testing.T) {
			var calls []int64
			svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, accountID int64) (*http.Response, error) {
				calls = append(calls, accountID)
				return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
			})
			hash, _ := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
			require.NoError(t, svc.BindStickySession(context.Background(), apiKey.GroupID, hash, 22))
			forward := func() {
				const path = "/alpha/notes/v2/write_file"
				c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, path)
				resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, path, codexAuxiliaryStickyTestBody)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
			}
			forward()
			repo := svc.accountRepo.(codexModelsVisibilityAccountRepo)
			accounts := repo.byGroup[*apiKey.GroupID]
			switch reason {
			case "subscription_expired":
				accounts[1].Credentials["subscription_expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
			case "unschedulable":
				accounts[1].Schedulable = false
			case "disabled":
				accounts[1].Status = "disabled"
			case "removed":
				repo.byGroup[*apiKey.GroupID] = accounts[:1]
			}
			forward()
			// Restoring the former account does not bounce the session back again.
			accounts[1].Credentials["subscription_expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
			accounts[1].Schedulable, accounts[1].Status = true, StatusActive
			repo.byGroup[*apiKey.GroupID] = accounts
			forward()
			require.Equal(t, []int64{22, 11, 11}, calls)
			require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + hash}], "auxiliary rebind must not affect Responses affinity")
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

func TestForwardCodexHistoryNotesReadsLegacyResponsesSeedWithoutWritingIt(t *testing.T) {
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
	require.NotContains(t, cache.bindings, codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, "openai:" + hash})
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
	require.Zero(t, accountID, "auxiliary calls must not write Responses affinity")
}
