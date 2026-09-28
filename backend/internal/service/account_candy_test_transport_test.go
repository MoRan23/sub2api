package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCandyResponseWriterOnlyFinalAndFailureSticky(t *testing.T) {
	tests := []struct {
		name, stream, want, failure string
		complete                    bool
	}{
		{"complete", "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"PRIVATE_REASONING\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"table\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "table", "", true},
		{"failure_then_complete", "data: {\"type\":\"response.failed\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "", "upstream_stream_failed", false},
		{"done_is_not_terminal", "data: [DONE]\n\n", "", "", false},
		{"tool", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\n", "", "unexpected_tool_call", false},
		{"commentary", "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"phase\":\"commentary\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"not final\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"final\"}]}]}}\n\n", "final", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newCandyResponseWriter(nil)
			for _, b := range []byte(tc.stream) {
				_, _ = w.Write([]byte{b})
			}
			w.finish()
			require.Equal(t, tc.want, w.answer())
			require.Equal(t, tc.failure, w.failure)
			require.Equal(t, tc.complete, w.completed && !w.failed)
			require.NotContains(t, w.answer(), "PRIVATE_REASONING")
		})
	}
}

func TestCandyResponseWriterLimit(t *testing.T) {
	w := newCandyResponseWriter(nil)
	for i := 0; i < 5; i++ {
		payload, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": strings.Repeat("x", 300000)})
		_, _ = w.Write(append(append([]byte("data: "), payload...), []byte("\n\n")...))
	}
	require.Equal(t, "response_too_large", w.failure)
	require.LessOrEqual(t, len(w.answer()), CandyTestMaxResponseBytes)
}

func TestCandyResponseWriterBoundsItemsAndHonorsFinalEnvelope(t *testing.T) {
	w := newCandyResponseWriter(nil)
	for i := 0; i < 129; i++ {
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":{\"type\":\"message\"}}\n\n", i)
	}
	require.Equal(t, "response_too_large", w.failure)
	require.Len(t, w.phases, 128)
	w = newCandyResponseWriter(nil)
	_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"not a final answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"phase\":\"commentary\",\"content\":[{\"type\":\"output_text\",\"text\":\"not a final answer\"}]}]}}\n\n")
	require.True(t, w.completed)
	require.Empty(t, w.answer())
}

func TestCandyPurposeOneSendAndContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	attempt := &openAICandyTestAttempt{}
	ctx = withOpenAICandyTest(ctx, attempt)
	for _, stream := range []bool{false, true} {
		detached, release := detachStreamUpstreamContext(ctx, stream)
		_, ok := detached.Deadline()
		require.True(t, ok)
		release()
	}
	detached, release := detachUpstreamContext(ctx)
	defer release()
	_, ok := detached.Deadline()
	require.True(t, ok)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://synthetic.invalid/responses", strings.NewReader(`{"model":"gpt-6-astra","reasoning":{"effort":"ultra"}}`))
	_, err := candyTestBeforeSend(req)
	require.NoError(t, err)
	_, err = candyTestBeforeSend(req)
	require.EqualError(t, err, "inference_replay_disabled")
	require.Equal(t, "gpt-6-astra", attempt.actualModel)
	require.Equal(t, "ultra", attempt.actualEffort)
	cancel()
	require.ErrorIs(t, detached.Err(), context.Canceled)
}

func TestCandyTransportHTTPFailuresNeverRetryOrChangeAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat"} {
		for _, status := range []int{400, 401, 403, 429, 500} {
			t.Run(path+http.StatusText(status), func(t *testing.T) {
				account := newOpenAIRejectedFieldTestAccount()
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
					account.Extra["openai_passthrough_enabled"] = true
				}
				if path == "chat" {
					account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
					account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeAuto)
				}
				repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(status, `{"error":{"message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`)}}
				gateway := newOpenAIRejectedFieldTestService(upstream)
				gateway.accountRepo = repo
				// Any unguarded repository mutation hits the embedded nil implementation.
				gateway.rateLimitService = &RateLimitService{accountRepo: repo}
				runner := newCandySyntheticCatalogTransport(repo, gateway)
				_, err := runner.Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
				require.Error(t, err)
				require.Len(t, upstream.bodies, 1)
				require.True(t, repo.accounts[0].Schedulable)
				require.Equal(t, StatusActive, repo.accounts[0].Status)
				require.False(t, repo.accounts[0].openAICandyTest, "the repository account must not be marked or mutated")
			})
		}
	}
}

func TestCandyTransportPreservesSelectedUpstreamModelAndObservesRawModel(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.5": "gpt-6-astra"}
	repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
	answer := "| 问题 | 最少数量 | 最优取法 |\n| 第1问 | 32 | 任意 |\n| 第2问 | 29 | 任意 |\n| 第3问固定 | 40 | 任意 |\n| 第3问自适应 | 38 | 任意 |"
	terminal, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "response_synthetic", "model": "gpt-6-luna", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}, "usage": map[string]int{"input_tokens": 2, "output_tokens": 3}}})
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(append(append([]byte("data: "), terminal...), []byte("\n\n")...)))}}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	runner := newCandySyntheticCatalogTransport(repo, gateway)
	result, err := runner.Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
	require.NoError(t, err)
	require.True(t, result.Completed)
	require.Equal(t, answer, result.ResponseText)
	require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "gpt-5.5", result.RequestedModel)
	require.Equal(t, "gpt-5.5", result.ActualModel)
	require.Equal(t, "gpt-6-luna", result.UpstreamModel)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "tools").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "previous_response_id").Exists())
	require.Equal(t, CandyTestPrompt, gjson.GetBytes(upstream.bodies[0], "input.0.content.0.text").String())
}

func TestCandyTokenExpiredDoesNotSuspend(t *testing.T) {
	account := &Account{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "synthetic", "expires_at": time.Now().Add(-time.Minute).Format(time.RFC3339)}}
	repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
	provider := &OpenAITokenProvider{accountRepo: repo}
	_, _, err := provider.getCandyTestAccessToken(withOpenAICandyTest(context.Background(), &openAICandyTestAttempt{}), account)
	require.EqualError(t, err, "authorization_expired")
	require.True(t, repo.accounts[0].Schedulable)
}

func TestCandySafeErrorNeverContainsProviderBody(t *testing.T) {
	err := safeCandyTestError(context.Background(), errors.New("Bearer synthetic-sensitive upstream arbitrary body"))
	require.EqualError(t, err, "upstream_failed")
}

func TestCandyOAuthDefaultSystemAndRelatedCredentialKinds(t *testing.T) {
	for _, family := range OpenAIOAuthOSFamilies() {
		t.Run(family, func(t *testing.T) {
			account := newOpenAIOAuthNamespaceTestAccount()
			profiles, err := BuildOpenAIOAuthOSProfiles(account, &OpenAIOAuthOSProfiles{DefaultOS: family})
			require.NoError(t, err)
			account.OpenAIOAuthOSProfiles = profiles
			repo := newAuthorizedOpenAIOAuthTestRepo(account)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(401, `{"error":{"message":"synthetic denied"}}`)}}
			gateway := newOpenAIRejectedFieldTestService(upstream, account)
			gateway.accountRepo = repo
			_, err = newCandySyntheticCatalogTransport(repo, gateway).Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-6-astra", PromptVersion: CandyTestPromptVersion})
			require.EqualError(t, err, "upstream_http_401")
			require.Len(t, upstream.bodies, 1)
			require.True(t, account.Schedulable)
			require.Equal(t, family, openai.DetectOSFamilyFromUserAgent(upstream.lastReq.Header.Get("User-Agent")))
		})
	}
	for _, kind := range []string{"setup-token", "pat", "agent"} {
		t.Run(kind, func(t *testing.T) {
			account := newOpenAIOAuthNamespaceTestAccount()
			switch kind {
			case "setup-token":
				account.Type = AccountTypeSetupToken
			case "pat":
				account.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
			default:
				_, key := newTestAgentIdentityKey(t)
				account.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModeAgentIdentity
				account.Credentials["agent_private_key"] = key
				account.Credentials["agent_runtime_id"] = "synthetic-runtime"
				account.Credentials["task_id"] = "synthetic-task"
			}
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(403, `{"error":{"message":"synthetic denied"}}`)}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			_, err := newCandySyntheticCatalogTransport(repo, gateway).Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-6-astra", PromptVersion: CandyTestPromptVersion})
			require.EqualError(t, err, "upstream_http_403")
			require.Len(t, upstream.bodies, 1)
			require.True(t, repo.accounts[0].Schedulable)
		})
	}
}

func TestCandySparkUsesParentDefaultIdentity(t *testing.T) {
	parent := newOpenAIOAuthNamespaceTestAccount()
	profiles, err := BuildOpenAIOAuthOSProfiles(parent, &OpenAIOAuthOSProfiles{DefaultOS: OpenAIOSMacOS})
	require.NoError(t, err)
	parent.OpenAIOAuthOSProfiles = profiles
	shadow := snapshotOAuthRefreshAccount(parent)
	shadow.ID = parent.ID + 1
	shadow.ParentAccountID = &parent.ID
	shadow.Credentials = map[string]any{"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"}}
	repo := newAuthorizedOpenAIOAuthTestRepo(parent)
	repo.accounts[shadow.ID] = shadow
	upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(429, `{"error":{"message":"synthetic denied"}}`)}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	_, err = newCandySyntheticCatalogTransport(repo, gateway).Execute(context.Background(), &CandyTestItem{AccountID: shadow.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
	require.EqualError(t, err, "upstream_http_429")
	require.Len(t, upstream.bodies, 1)
	require.Equal(t, OpenAIOSMacOS, openai.DetectOSFamilyFromUserAgent(upstream.lastReq.Header.Get("User-Agent")))
	require.True(t, parent.Schedulable)
	require.True(t, shadow.Schedulable)
}

type candyRefreshExecutor struct {
	fail  bool
	calls int
}

func (e *candyRefreshExecutor) CanRefresh(*Account) bool                  { return true }
func (e *candyRefreshExecutor) NeedsRefresh(*Account, time.Duration) bool { return true }
func (e *candyRefreshExecutor) CacheKey(*Account) string                  { return "synthetic-refresh" }
func (e *candyRefreshExecutor) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	e.calls++
	if e.fail {
		return nil, errors.New("synthetic failure contains secret")
	}
	next := shallowCopyMap(account.Credentials)
	next["access_token"] = "new-synthetic"
	next["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return next, nil
}

type candyRefreshRepo struct {
	stubOpenAIAccountRepo
	patches int
}

func (r *candyRefreshRepo) PatchOpenAIOAuthCredentialsIfUnchanged(_ context.Context, id int64, expected map[string]any, proxy *int64, patch map[string]any, removed []string) (bool, error) {
	r.patches++
	for key, value := range expected {
		if !reflect.DeepEqual(value, r.accounts[0].Credentials[key]) {
			return false, nil
		}
	}
	for key, value := range patch {
		r.accounts[0].Credentials[key] = value
	}
	for _, key := range removed {
		delete(r.accounts[0].Credentials, key)
	}
	return true, nil
}
func TestCandyRefreshUsesCASWithoutFailureFallback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old-synthetic", "refresh_token": "synthetic-refresh", "expires_at": time.Now().Add(-time.Minute).Format(time.RFC3339)}}
			repo := &candyRefreshRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*account}}}
			executor := &candyRefreshExecutor{fail: fail}
			provider := &OpenAITokenProvider{accountRepo: repo, refreshAPI: NewOAuthRefreshAPI(repo, nil), executor: executor}
			token, _, err := provider.getCandyTestAccessToken(withOpenAICandyTest(context.Background(), &openAICandyTestAttempt{}), account)
			if fail {
				require.EqualError(t, err, "refresh_failed")
				require.Empty(t, token)
				require.Zero(t, repo.patches)
			} else {
				require.NoError(t, err)
				require.Equal(t, "new-synthetic", token)
				require.Equal(t, 1, repo.patches)
			}
			require.Equal(t, 1, executor.calls)
			require.True(t, repo.accounts[0].Schedulable)
			require.Equal(t, StatusActive, repo.accounts[0].Status)
		})
	}
}

func TestCandyAgentIdentityKeyReplacementIsAuthorizationChange(t *testing.T) {
	before := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{openAIAuthModeCredentialKey: OpenAIAuthModeAgentIdentity, "agent_private_key": "old-synthetic", "agent_runtime_id": "runtime", "task_id": ""}}
	after := snapshotOAuthRefreshAccount(before)
	after.Credentials["task_id"] = "initialized-task"
	require.True(t, sameCandyStaticAuthorization(before, after))
	after.Credentials["agent_private_key"] = "replacement-synthetic"
	require.False(t, sameCandyStaticAuthorization(before, after))
}

func TestCandyAgentTaskCASCannotOverwriteReplacementKey(t *testing.T) {
	old := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{openAIAuthModeCredentialKey: OpenAIAuthModeAgentIdentity, "agent_private_key": "old", "agent_runtime_id": "r"}}
	current := snapshotOAuthRefreshAccount(old)
	current.Credentials["agent_private_key"] = "new"
	repo := &candyRefreshRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*current}}}
	err := persistCandyAgentIdentityTask(context.Background(), repo, old, "new-task")
	require.EqualError(t, err, "authorization_changed")
	require.Equal(t, "new", repo.accounts[0].GetCredential("agent_private_key"))
	require.Empty(t, repo.accounts[0].GetCredential("task_id"))
}

func TestCandyAgentTaskLockHonorsCancellation(t *testing.T) {
	account := &Account{ID: 987654321, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{openAIAuthModeCredentialKey: OpenAIAuthModeAgentIdentity}}
	mu := &sync.Mutex{}
	mu.Lock()
	defer mu.Unlock()
	agentIdentityTaskLocks.Store(account.ID, mu)
	defer agentIdentityTaskLocks.Delete(account.ID)
	ctx, cancel := context.WithCancel(withOpenAICandyTest(context.Background(), &openAICandyTestAttempt{}))
	cancel()
	err := ensureAgentIdentityTaskForAccount(ctx, nil, nil, &sync.Mutex{}, account, "")
	require.ErrorIs(t, err, context.Canceled)
}
