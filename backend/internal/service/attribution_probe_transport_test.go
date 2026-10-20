package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func attributionSingleAttemptContext() context.Context {
	return context.WithValue(context.Background(), attributionExecutionContextKey{}, true)
}

func TestAttributionProbeHTTPStatusAndRetryAfterSingleAttempt(t *testing.T) {
	for _, mode := range []string{"generic", "passthrough", "chat", "codex_engine", "oauth"} {
		for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s/%d", mode, status), func(t *testing.T) {
				account := newOpenAIRejectedFieldTestAccount()
				account.Extra["openai_api_key_mode"] = mode
				if mode == "passthrough" {
					account.Extra["openai_passthrough_enabled"] = true
					account.Extra["openai_passthrough"] = true
				}
				if mode == "chat" {
					account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
				}
				response := newOpenAIRejectedFieldTestResponse(status, `{"error":{"code":"rate_limit_exceeded","message":"synthetic private text"}}`)
				response.Header.Set("Retry-After", "120")
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
				gateway := newOpenAIRejectedFieldTestService(upstream)
				var repo AccountRepository = &stubOpenAIAccountRepo{accounts: []Account{*account}}
				if mode == "oauth" {
					account = newOpenAIOAuthNamespaceTestAccount()
					repo = newAuthorizedOpenAIOAuthTestRepo(account)
				}
				gateway.accountRepo = repo
				gateway.rateLimitService = &RateLimitService{accountRepo: repo}
				result, err := NewAccountCandyTestTransport(repo, gateway).Probe(attributionSingleAttemptContext(), account.ID, "gpt-6-astra", "synthetic challenge")
				require.EqualError(t, err, fmt.Sprintf("upstream_http_%d", status))
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1, "durable attribution, not transport, owns retries")
				require.Zero(t, result.Retries)
				gotStatus, wait := AttributionProbeRetryStatus(err, result)
				require.Equal(t, status, gotStatus)
				require.Equal(t, 120*time.Second, wait)
				require.Equal(t, status == http.StatusTooManyRequests, diagnosticRateLimitError(err))
				require.NotContains(t, err.Error(), "synthetic private text")
			})
		}
	}
}

func TestAttributionCodexEngineDeployment503PreservesBusinessCooldown(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{
			name:        "JSON maintenance",
			contentType: "application/json",
			body:        `{"error":{"code":"deployment_maintenance","message":"synthetic maintenance"},"usage":{"input_tokens":11,"output_tokens":2}}`,
		},
		{
			name:        "SSE capacity",
			contentType: "text/event-stream",
			body:        "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"status\":503,\"code\":\"capacity_unavailable\",\"message\":\"synthetic capacity\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":2}}}\n\n",
		},
	} {
		for _, existingCooldown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", tc.name, existingCooldown), func(t *testing.T) {
				account := newOpenAIRejectedFieldTestAccount()
				account.ID = 42
				account.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine"
				repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
				response := &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tc.body)),
				}
				response.Header.Set("Content-Type", tc.contentType)
				response.Header.Set("Retry-After", "120")
				response.Header.Set(codexEngineUnavailableScopeHeader, "deployment")
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
				gateway := newOpenAIRejectedFieldTestService(upstream)
				gateway.accountRepo = repo
				gateway.rateLimitService = &RateLimitService{accountRepo: repo}
				// Preserve a separate account's business cooldown as well as the
				// selected account's shorter deadline: a diagnostic must neither
				// extend the latter nor clear either entry.
				wantCooldowns := map[int64]time.Time{account.ID + 1: time.Now().Add(time.Minute)}
				if existingCooldown {
					wantCooldowns[account.ID] = time.Now().Add(5 * time.Second)
				}
				gateway.codexEngineDeploymentCooldowns.until = make(map[int64]time.Time, len(wantCooldowns))
				for id, until := range wantCooldowns {
					gateway.codexEngineDeploymentCooldowns.until[id] = until
				}

				result, err := NewAccountCandyTestTransport(repo, gateway).Probe(attributionSingleAttemptContext(), account.ID, "gpt-6-astra", "synthetic challenge")

				require.EqualError(t, err, "upstream_http_503")
				require.NotNil(t, result)
				require.False(t, result.Completed)
				require.Zero(t, result.Retries)
				require.Len(t, upstream.requests, 1, "durable attribution, not transport, owns retries")
				status, wait := AttributionProbeRetryStatus(err, result)
				require.Equal(t, http.StatusServiceUnavailable, status)
				require.Equal(t, 120*time.Second, wait)
				require.False(t, diagnosticRateLimitError(err))
				require.NotNil(t, result.Usage)
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Equal(t, wantCooldowns, gateway.codexEngineDeploymentCooldowns.until)
				require.True(t, repo.accounts[0].Schedulable)
				require.Equal(t, StatusActive, repo.accounts[0].Status)
				require.False(t, repo.accounts[0].openAICandyTest)
			})
		}
	}
}

func TestAttributionProbeSSEStatusPriorityAndLegacyPelican(t *testing.T) {
	for _, tc := range []struct {
		name, event, code string
		status            int
	}{
		{"response503", `{"type":"response.failed","response":{"error":{"status_code":503,"code":"rate_limit_exceeded"}}}`, "upstream_http_503", 503},
		{"error503", `{"type":"error","status":503,"error":{"code":"rate_limit_exceeded"}}`, "upstream_http_503", 503},
		{"nested_string503", `{"type":"error","error":{"status":"503","code":"rate_limit_exceeded"}}`, "upstream_http_503", 503},
		{"response429", `{"type":"response.failed","response":{"error":{"status_code":429,"code":"server_error"}}}`, "upstream_http_429", 429},
		{"implicit_rate", `{"type":"error","error":{"code":"rate_limit_exceeded"}}`, "account_rate_limited", 0},
		{"prose503", `{"type":"error","error":{"message":"HTTP 503 temporarily unavailable"}}`, "upstream_stream_failed", 0},
		{"fraction503", `{"type":"error","error":{"status":503.1,"code":"rate_limit_exceeded"}}`, "account_rate_limited", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := newCandyResponseWriter(nil)
			writer.attribution = true
			_, err := io.WriteString(writer, "data: "+tc.event+"\n\n")
			require.NoError(t, err)
			require.Equal(t, tc.code, writer.failure)
			require.Equal(t, tc.status, writer.errorStatus)
			require.False(t, writer.completed)
		})
	}
	legacy := newCandyResponseWriter(nil)
	_, err := io.WriteString(legacy, `data: {"type":"error","status":503,"error":{"code":"rate_limit_exceeded"}}`+"\n\n")
	require.NoError(t, err)
	require.Equal(t, "account_rate_limited", legacy.failure, "Pelican keeps its existing classification")
}

func TestAttributionProbeSSE503SurvivesGatewaySemanticFailover(t *testing.T) {
	for _, mode := range []string{"generic", "passthrough", "codex_engine"} {
		t.Run(mode, func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra["openai_api_key_mode"] = mode
			account.Extra["openai_passthrough_enabled"] = mode == "passthrough"
			account.Extra["openai_passthrough"] = mode == "passthrough"
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Retry-After": {"90"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.failed","response":{"error":{"status":503,"code":"rate_limit_exceeded","message":"synthetic"},"usage":{"input_tokens":11,"output_tokens":0}}}` + "\n\n"))}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			result, err := NewAccountCandyTestTransport(repo, gateway).Probe(attributionSingleAttemptContext(), account.ID, "gpt-6-astra", "challenge")
			require.EqualError(t, err, "upstream_http_503")
			status, wait := AttributionProbeRetryStatus(err, result)
			require.Equal(t, 503, status)
			require.Equal(t, 90*time.Second, wait)
			require.Len(t, upstream.requests, 1)
			require.NotNil(t, result.Usage)
			require.Equal(t, 11, result.Usage.InputTokens)
		})
	}
}

func TestAttributionRetryStatusDoesNotTreatInferredCapacityAsExplicit503(t *testing.T) {
	err := &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"type":"error","error":{"code":"server_is_overloaded"}}`)}
	status, _ := AttributionProbeRetryStatus(err, nil)
	require.Zero(t, status)
	safe := safeAttributionProbeError(context.Background(), err)
	require.EqualError(t, safe, "upstream_stream_failed")
	status, _ = AttributionProbeRetryStatus(safe, nil)
	require.Zero(t, status)
	require.True(t, retryableDiagnosticError(safe), "non-explicit capacity errors retain bounded transient retries")
	engine := newCodexEngineResponseError(200, []byte(`{"type":"response.failed","response":{"error":{"status":503.1}}}`), "response.failed")
	require.Equal(t, 503, engine.reportedStatus, "legacy Engine semantic parser is deliberately unchanged")
	status, _ = AttributionProbeRetryStatus(engine, nil)
	require.Zero(t, status)
	require.EqualError(t, safeAttributionProbeError(context.Background(), engine), "upstream_stream_failed")
}

func TestAttributionProbeChatBridgeSSEStatus(t *testing.T) {
	for _, status := range []int{503, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			payload := fmt.Sprintf(`data: {"error":{"status_code":%d,"code":"rate_limit_exceeded"},"usage":{"prompt_tokens":9,"completion_tokens":1}}`+"\n\n", status)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Retry-After": {"77"}}, Body: io.NopCloser(strings.NewReader(payload))}}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			result, err := NewAccountCandyTestTransport(repo, gateway).Probe(attributionSingleAttemptContext(), account.ID, "gpt-5.5", "challenge")
			require.EqualError(t, err, fmt.Sprintf("upstream_http_%d", status))
			gotStatus, delay := AttributionProbeRetryStatus(err, result)
			require.Equal(t, status, gotStatus)
			require.Equal(t, 77*time.Second, delay)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
			require.Equal(t, 9, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
		})
	}
}

func TestAttributionRetryAfterProtocolValues(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0}, {"0", 0}, {"2", 2 * time.Second}, {" 120 ", 120 * time.Second},
		{"301", 300 * time.Second}, {"99999999999999999999999999999", 300 * time.Second},
		{"-1", 0}, {"+2", 0}, {"1.5", 0}, {"later", 0},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(9 * time.Minute).Format(http.TimeFormat), 300 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		t.Run(tc.value, func(t *testing.T) { require.Equal(t, tc.want, attributionRetryAfter(tc.value, now)) })
	}
}

func TestAttributionProbeMetadataChangesDoNotCancelButUpstreamIdentityDoes(t *testing.T) {
	for _, change := range []string{"operational", "mapping", "base_url", "api_key", "engine_mode", "auth_type"} {
		t.Run(change, func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			body := &candyAuthorizationResponseReader{Reader: strings.NewReader(candyAuthorizationCompletedResponse), beforeRead: func() {
				a := &repo.accounts[0]
				switch change {
				case "operational":
					past := time.Now().Add(-time.Hour)
					a.Name, a.Status, a.Schedulable, a.ExpiresAt, a.Priority = "renamed", "disabled", false, &past, 200
					a.GroupIDs = []int64{999}
				case "mapping":
					a.Credentials["model_mapping"] = map[string]any{"gpt-5.5": "different-model"}
				case "base_url":
					a.Credentials["base_url"] = "https://replacement.invalid"
				case "api_key":
					a.Credentials["api_key"] = "synthetic-replacement"
				case "engine_mode":
					a.Extra["openai_api_key_mode"] = "codex_engine"
				case "auth_type":
					a.Type = AccountTypeOAuth
				}
			}}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			result, err := NewAccountCandyTestTransport(repo, gateway).Probe(attributionSingleAttemptContext(), account.ID, "gpt-5.5", "challenge")
			if change == "operational" || change == "mapping" {
				require.NoError(t, err)
				require.True(t, result.Completed)
			} else {
				require.EqualError(t, err, "authorization_changed")
			}
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.bodies[0], "model").String())
		})
	}
}

func TestAttributionProbeAuthorizationSurvivesRetryAndFencesBeforeSend(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprint(replaced), func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIRejectedFieldTestResponse(503, `{"error":{"code":"rate_limit_exceeded"}}`),
				{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(candyAuthorizationCompletedResponse))},
			}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			runner := NewAccountCandyTestTransport(repo, gateway)
			first, err := runner.Probe(attributionSingleAttemptContext(), account.ID, "gpt-5.5", "challenge")
			require.EqualError(t, err, "upstream_http_503")
			require.NotEmpty(t, first.AttributionAuthorization)
			ctx := context.WithValue(attributionSingleAttemptContext(), attributionProbeAuthorizationKey{}, first.AttributionAuthorization)
			if replaced {
				repo.accounts[0].Credentials["api_key"] = "synthetic-replacement"
			}
			second, err := runner.Probe(ctx, account.ID, "gpt-5.5", "challenge")
			if replaced {
				require.EqualError(t, err, "authorization_changed")
				require.Len(t, upstream.requests, 1, "a resumed checkpoint must not send using replacement credentials")
			} else {
				require.NoError(t, err)
				require.Equal(t, first.AttributionAuthorization, second.AttributionAuthorization)
				require.Len(t, upstream.requests, 2)
			}
		})
	}
	account := newOpenAIRejectedFieldTestAccount()
	repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
	upstream := &httpUpstreamRecorder{}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	checks := 0
	ctx := context.WithValue(attributionSingleAttemptContext(), diagnosticRetryValidationKey{}, func(context.Context) error {
		checks++
		return candyTestError("authorization_changed")
	})
	_, err := NewAccountCandyTestTransport(repo, gateway).Probe(ctx, account.ID, "gpt-5.5", "challenge")
	require.EqualError(t, err, "authorization_changed")
	require.Positive(t, checks)
	require.Empty(t, upstream.requests, "persisted execution fence must run at the physical send boundary")
}

func TestAttributionProbeAuthorizationPinsOwnerGenerationAndOSNotTokenRevision(t *testing.T) {
	business := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	credential := &Account{ID: 24, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		OpenAIOAuthCredentialOwnerID: 24, OpenAIOAuthAuthorizationGeneration: "original-grant", OpenAIOAuthCredentialOS: "windows",
		Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "chatgpt_account_id": "same-account"}}
	digest := attributionProbeAuthorization(business, credential)
	refreshed := snapshotOAuthRefreshAccount(credential)
	refreshed.Credentials["access_token"], refreshed.Credentials["refresh_token"] = "new", "new-refresh"
	refreshed.OpenAIOAuthCredentialRevision++
	require.Equal(t, digest, attributionProbeAuthorization(business, refreshed))
	for _, mutate := range []func(*Account){
		func(a *Account) { a.ID++ },
		func(a *Account) { a.OpenAIOAuthCredentialOwnerID++ },
		func(a *Account) { a.OpenAIOAuthAuthorizationGeneration = "new-grant" },
		func(a *Account) { a.OpenAIOAuthCredentialOS = "linux" },
		func(a *Account) { a.Credentials["chatgpt_account_id"] = "other-account" },
	} {
		changed := snapshotOAuthRefreshAccount(credential)
		mutate(changed)
		require.NotEqual(t, digest, attributionProbeAuthorization(business, changed))
	}
}
