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

func TestForwardCodexHistoryNotesDoesNotBindIncompleteFallbackResponse(t *testing.T) {
	for _, failure := range []string{"eof", "timeout", "invalid_json", "oversize"} {
		t.Run(failure, func(t *testing.T) {
			var calls []int64
			svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
				calls = append(calls, id)
				if id == 22 {
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
				}
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
			require.Equal(t, []int64{22, 11}, calls)
			require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
			_, stored := svc.codexAuxiliarySticky.Load(codexAuxiliaryLocalStickyKey{groupID: *apiKey.GroupID, sessionHash: hash})
			require.False(t, stored)
		})
	}
}

func TestForwardCodexHistoryNotesBindsOnlyAfterCompleteJSON(t *testing.T) {
	var svc *OpenAIGatewayService
	var apiKey *APIKey
	var cache *codexAuxiliaryStickyTestCache
	var responseContext context.Context
	var calls []int64
	hash, _ := deriveOpenAISessionHashes(codexAuxiliaryStickyTestSession)
	svc, apiKey, cache = newCodexAuxiliaryStickyTestService(t, func(req *http.Request, id int64) (*http.Response, error) {
		calls = append(calls, id)
		if id == 22 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		responseContext = req.Context()
		resp := codexAuxiliaryStickyTestResponse(http.StatusOK)
		reader := strings.NewReader(`{"encrypted_output":"opaque", "items":[]}`)
		resp.Body = io.NopCloser(codexAuxiliaryReaderFunc(func(p []byte) (int, error) {
			require.NoError(t, req.Context().Err(), "deadline remains active until response EOF")
			require.Equal(t, int64(22), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}], "reading headers or partial body cannot bind")
			return reader.Read(p)
		}))
		return resp, nil
	})
	require.NoError(t, svc.BindStickySession(context.Background(), apiKey.GroupID, hash, 22))
	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/notes/v2/read_file")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/notes/v2/read_file", codexAuxiliaryStickyTestBody)
	require.NoError(t, err)
	require.Equal(t, []int64{22, 11}, calls)
	require.ErrorIs(t, responseContext.Err(), context.Canceled)
	require.Equal(t, int64(11), cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"encrypted_output":"opaque", "items":[]}`, string(got))
	require.NoError(t, resp.Body.Close())
}

func TestForwardCodexHistoryNotesWritesRetryOnlyBeforeConnection(t *testing.T) {
	for _, path := range []string{"/alpha/notes/v2/append_to_file", "/alpha/notes/v2/write_file"} {
		for _, failure := range []string{"dial", "timeout", "eof", "body_eof", "invalid_json", "503", "cancelled", "request_error"} {
			t.Run(path+"/"+failure, func(t *testing.T) {
				var calls []int64
				svc, apiKey, cache := newCodexAuxiliaryStickyTestService(t, func(_ *http.Request, id int64) (*http.Response, error) {
					calls = append(calls, id)
					if id == 11 {
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
				wantAccount := int64(22)
				if failure == "dial" {
					require.NoError(t, err)
					require.Equal(t, []int64{22, 11}, calls)
					wantAccount = 11
				} else {
					require.Equal(t, []int64{22}, calls)
					if failure == "503" {
						require.NoError(t, err)
						require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
					} else {
						require.Error(t, err)
					}
				}
				if resp != nil {
					require.NoError(t, resp.Body.Close())
				}
				require.Equal(t, wantAccount, cache.bindings[codexAuxiliaryStickyTestCacheKey{*apiKey.GroupID, svc.openAISessionCacheKey(hash)}])
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
