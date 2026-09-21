//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func accountTestResponseInfoOAuth(plan string) *Account {
	return &Account{
		ID: 891, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Concurrency: 1, Credentials: map[string]any{"access_token": "test-token", "plan_type": plan},
	}
}

// The public SSE contract must emit one safe summary before its terminal event.
func requireAccountTestResponseInfo(t *testing.T, body, terminalType string) gjson.Result {
	t.Helper()
	var info gjson.Result
	infoCount, infoIndex, terminalIndex := 0, -1, -1
	for index, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := gjson.Parse(strings.TrimPrefix(line, "data: "))
		switch event.Get("type").String() {
		case "response_info":
			info = event.Get("data")
			infoIndex = index
			infoCount++
		case terminalType:
			terminalIndex = index
		}
	}
	require.Equal(t, 1, infoCount, body)
	require.Greater(t, terminalIndex, infoIndex, body)
	return info
}

func TestAccountTestResponseInfo_OAuthShapeAndActualModel(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, plan, token, shape, reason string
		expectedLength                   int
	}{
		{"personal_target", "plus", codexStateTestToken(10, now), "target", "", 292},
		{"personal_extended", "pro", codexStateTestToken(11, now), "extended", "", 292},
		{"team_target", "team", codexStateTestToken(12, now), "target", "", 332},
		{"business_extended", "self_serve_business_prolite", codexStateTestToken(13, now), "extended", "", 332},
		{"unknown_plan_not_inferred_from_length", "future_plan", codexStateTestToken(10, now), "unknown", "account_type_unknown", 0},
		{"wrong_account_shape", "plus", codexStateTestToken(12, now), "unknown", "unexpected_shape", 292},
		{"invalid_envelope_with_target_length", "plus", strings.Repeat("!", 292), "unknown", "invalid_envelope", 292},
		{"expired_target", "plus", codexStateTestToken(10, now.Add(-2*time.Hour)), "unknown", "expired", 292},
		{"future_target", "plus", codexStateTestToken(10, now.Add(2*time.Minute)), "unknown", "future_issued_at", 292},
		{"missing_state", "plus", "", "missing", "", 292},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder := newTestContext()
			response := newJSONResponse(http.StatusOK,
				"data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-astra-returned\"}}\n\n"+
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"+
					"data: {\"type\":\"response.completed\"}\n\n")
			if tc.token != "" {
				response.Header.Set("x-codex-turn-state", tc.token)
			}
			upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
			service := &AccountTestService{httpUpstream: upstream}
			account := accountTestResponseInfoOAuth(tc.plan)
			require.False(t, CodexTurnStateConfigForAccount(account).Enabled, "diagnostics also work with caching disabled")
			require.NoError(t, service.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", ""))
			require.Len(t, upstream.requests, 1)
			requestBody, err := io.ReadAll(upstream.requests[0].Body)
			require.NoError(t, err)
			require.Equal(t, "gpt-6-astra", gjson.GetBytes(requestBody, "model").String())
			info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
			require.Equal(t, "gpt-6-astra-returned", info.Get("upstream_model").String())
			require.Equal(t, int64(len(tc.token)), info.Get("codex_turn_state.length").Int())
			require.Equal(t, int64(tc.expectedLength), info.Get("codex_turn_state.expected_length").Int())
			require.Equal(t, tc.shape, info.Get("codex_turn_state.shape").String())
			require.Equal(t, tc.reason, info.Get("codex_turn_state.validation_reason").String())
			if tc.token != "" {
				require.Equal(t, "header", info.Get("codex_turn_state.source").String())
				require.NotContains(t, recorder.Body.String(), tc.token)
			}
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestAccountTestResponseInfo_MetadataAndHeaderCandidatePriority(t *testing.T) {
	now := time.Now().UTC()
	target, extended := codexStateTestToken(10, now), codexStateTestToken(11, now)
	for _, tc := range []struct {
		name, header, metadata, shape, source string
		length                                int
	}{
		{"metadata_only", "", target, "target", "metadata", 292},
		{"metadata_target_over_header_extended", extended, target, "target", "metadata", 292},
		{"header_target_over_metadata_extended", target, extended, "target", "header", 292},
		{"valid_extended_over_invalid_target_length", strings.Repeat("!", 292), extended, "extended", "metadata", 312},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata, err := json.Marshal(map[string]any{
				"type": "response.metadata", "headers": map[string]any{"X-Codex-Turn-State": []string{tc.metadata}},
			})
			require.NoError(t, err)
			response := newJSONResponse(http.StatusOK,
				"data: "+string(metadata)+"\n\n"+
					"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra-upstream\"}}\n\n")
			response.Header.Set("x-codex-turn-state", tc.header)
			upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
			service := &AccountTestService{httpUpstream: upstream}
			ctx, recorder := newTestContext()
			require.NoError(t, service.testOpenAIAccountConnection(ctx, accountTestResponseInfoOAuth("plus"), "gpt-6-astra", "", ""))
			info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
			require.Equal(t, "gpt-6-astra-upstream", info.Get("upstream_model").String())
			require.Equal(t, tc.shape, info.Get("codex_turn_state.shape").String())
			require.Equal(t, int64(tc.length), info.Get("codex_turn_state.length").Int())
			require.Equal(t, tc.source, info.Get("codex_turn_state.source").String())
			require.NotContains(t, recorder.Body.String(), target)
			require.NotContains(t, recorder.Body.String(), extended)
		})
	}
}

func TestAccountTestResponseInfo_DoesNotInferMissingModelOrSearchOutputForState(t *testing.T) {
	token := codexStateTestToken(10, time.Now().UTC())
	// A token-like string in an output item is not a protocol response carrier.
	response := newJSONResponse(http.StatusOK, fmt.Sprintf(
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"model\":\"fake-model\",\"headers\":{\"x-codex-turn-state\":%q}}]}}\n\n", token))
	service := &AccountTestService{httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{response}}}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, accountTestResponseInfoOAuth("plus"), "gpt-6-astra", "", ""))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Empty(t, info.Get("upstream_model").String(), "selected request model must not be substituted for an absent returned model")
	require.Equal(t, "missing", info.Get("codex_turn_state.shape").String())
	require.Zero(t, info.Get("codex_turn_state.length").Int())
	require.NotContains(t, recorder.Body.String(), token)
}

func TestAccountTestResponseInfo_ShadowUsesCredentialParentPlan(t *testing.T) {
	parent := accountTestResponseInfoOAuth("team")
	shadow := &Account{
		ID: 892, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		ParentAccountID: &parent.ID, QuotaDimension: QuotaDimensionSpark, Concurrency: 1,
		Credentials: map[string]any{"plan_type": "plus"},
	}
	response := newJSONResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.3-codex-spark\"}}\n\n")
	response.Header.Set("x-codex-turn-state", codexStateTestToken(12, time.Now().UTC()))
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{parent.ID: parent, shadow.ID: shadow},
	}}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
	service := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	ctx, recorder := newTestContext()
	require.NoError(t, service.TestAccountConnection(ctx, shadow.ID, "gpt-5.3-codex-spark", "", ""))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, int64(332), info.Get("codex_turn_state.expected_length").Int())
	require.Equal(t, "target", info.Get("codex_turn_state.shape").String())
	require.Equal(t, "Bearer test-token", upstream.requests[0].Header.Get("Authorization"))
}

func TestAccountTestResponseInfo_FailedStreamRetainsFailureSemantics(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.failed","response":{"model":"gpt-6-astra-error","error":{"message":"synthetic upstream failure"}}}`,
		`{"type":"error","model":"gpt-6-astra-error","error":{"message":"synthetic upstream failure"}}`,
	} {
		t.Run(gjson.Get(terminal, "type").String(), func(t *testing.T) {
			token := codexStateTestToken(10, time.Now().UTC())
			response := newJSONResponse(http.StatusOK, "data: "+terminal+"\n\n"+
				"data: {\"type\":\"response.completed\"}\n\n")
			response.Header.Set("x-codex-turn-state", token)
			service := &AccountTestService{httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{response}}}
			ctx, recorder := newTestContext()
			require.Error(t, service.testOpenAIAccountConnection(ctx, accountTestResponseInfoOAuth("plus"), "gpt-6-astra", "", ""))
			info := requireAccountTestResponseInfo(t, recorder.Body.String(), "error")
			require.Equal(t, "gpt-6-astra-error", info.Get("upstream_model").String())
			require.Equal(t, "target", info.Get("codex_turn_state.shape").String())
			require.NotContains(t, recorder.Body.String(), `"success":true`)
			require.NotContains(t, recorder.Body.String(), token)
		})
	}
}

func TestAccountTestResponseInfo_APIKeyChatReportsActualModelOnly(t *testing.T) {
	account := &Account{
		ID: 893, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "synthetic-api-key", "base_url": "https://example.com"},
		Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: false},
	}
	response := newJSONResponse(http.StatusOK,
		"data: {\"model\":\"returned-chat-model\",\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	response.Header.Set("x-codex-turn-state", codexStateTestToken(10, time.Now().UTC()))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
	service := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", ""))
	require.Equal(t, "/v1/chat/completions", upstream.requests[0].URL.Path)
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "returned-chat-model", info.Get("upstream_model").String())
	require.False(t, info.Get("codex_turn_state").Exists(), "API key accounts must not be assigned an OAuth target length")
}

func TestAccountTestResponseInfo_CompactOAuthReportsResponse(t *testing.T) {
	response := newJSONResponse(http.StatusOK,
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"id\":\"cmp_probe\",\"encrypted_content\":\"synthetic\"}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra-compact\",\"output\":[]}}\n\n")
	response.Header.Set("x-codex-turn-state", codexStateTestToken(10, time.Now().UTC()))
	service := &AccountTestService{httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{response}}}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, accountTestResponseInfoOAuth("plus"), "gpt-6-astra", "", AccountTestModeCompact))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "gpt-6-astra-compact", info.Get("upstream_model").String())
	require.Equal(t, "target", info.Get("codex_turn_state.shape").String())
}
