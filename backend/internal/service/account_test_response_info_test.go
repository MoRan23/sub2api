//go:build unit

package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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

// The public SSE contract emits one safe summary before its terminal event.
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
	require.False(t, info.Get("codex_turn_state").Exists())
	require.False(t, info.Get("cookies").Exists())
	return info
}

func TestAccountTestResponseInfo_OAuthActualModelWithoutTicketDiagnostics(t *testing.T) {
	for _, plan := range []string{"plus", "pro", "team", "self_serve_business_prolite", "future_plan"} {
		t.Run(plan, func(t *testing.T) {
			ctx, recorder := newTestContext()
			response := newJSONResponse(http.StatusOK,
				"data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-astra-returned\"}}\n\n"+
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"+
					"data: {\"type\":\"response.completed\"}\n\n")
			response.Header.Set("x-codex-turn-state", "synthetic-opaque-must-not-be-exposed")
			response.Header.Set("Set-Cookie", "__oailb=private-cookie; Path=/")
			response.Header.Set("x-codex-safety-buffering-enabled", "true")
			response.Header.Set("x-codex-safety-buffering-faster-model", "gpt-5.6-luna")
			upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
			service := &AccountTestService{httpUpstream: upstream}
			require.NoError(t, service.testOpenAIAccountConnection(ctx, prepareAccountTestCredential(t, service, accountTestResponseInfoOAuth(plan)), "gpt-6-astra", "", ""))
			require.Len(t, upstream.requests, 1)
			requestBody, err := io.ReadAll(upstream.requests[0].Body)
			require.NoError(t, err)
			require.Equal(t, "gpt-6-astra", gjson.GetBytes(requestBody, "model").String())
			info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
			require.Equal(t, "gpt-6-astra-returned", info.Get("upstream_model").String())
			require.True(t, info.Get("response_evidence.safety_buffering_enabled").Bool())
			require.Equal(t, "gpt-5.6-luna", info.Get("response_evidence.safety_buffering_faster_model").String())
			require.Equal(t, "response", info.Get("response_evidence.header_evidence_scope").String())
			require.Equal(t, "unknown", info.Get("response_evidence.model_relation").String())
			require.NotContains(t, recorder.Body.String(), "synthetic-opaque-must-not-be-exposed")
			require.NotContains(t, recorder.Body.String(), "private-cookie")
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestAccountTestResponseInfo_MetadataObservesModelAndSafeHeadersOnly(t *testing.T) {
	response := newJSONResponse(http.StatusOK,
		"data: {\"type\":\"response.metadata\",\"headers\":{\"X-Codex-Turn-State\":[\"opaque-metadata\"],\"Set-Cookie\":\"__oailb=secret\",\"x-codex-safety-buffering-enabled\":\"false\"}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra-upstream\"}}\n\n")
	response.Header.Set("x-codex-turn-state", "opaque-header")
	upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
	service := &AccountTestService{httpUpstream: upstream}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, prepareAccountTestCredential(t, service, accountTestResponseInfoOAuth("plus")), "gpt-6-astra", "", ""))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "gpt-6-astra-upstream", info.Get("upstream_model").String())
	require.True(t, info.Get("response_evidence.safety_buffering_enabled").Exists())
	require.False(t, info.Get("response_evidence.safety_buffering_enabled").Bool())
	require.NotContains(t, recorder.Body.String(), "opaque-metadata")
	require.NotContains(t, recorder.Body.String(), "opaque-header")
	require.NotContains(t, recorder.Body.String(), "__oailb=secret")
}

func TestAccountTestResponseInfo_DoesNotInferMissingModelOrSearchOutput(t *testing.T) {
	token := "synthetic-opaque-output"
	response := newJSONResponse(http.StatusOK, fmt.Sprintf(
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"model\":\"fake-model\",\"headers\":{\"x-codex-turn-state\":%q}}]}}\n\n", token))
	service := &AccountTestService{httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{response}}}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, prepareAccountTestCredential(t, service, accountTestResponseInfoOAuth("plus")), "gpt-6-astra", "", ""))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Empty(t, info.Get("upstream_model").String())
	require.Equal(t, "not_reported", info.Get("response_evidence.model_relation").String())
	require.NotContains(t, recorder.Body.String(), token)
}

func TestAccountTestResponseInfo_ShadowUsesCredentialParent(t *testing.T) {
	parent := accountTestResponseInfoOAuth("team")
	shadow := &Account{
		ID: 892, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		ParentAccountID: &parent.ID, QuotaDimension: QuotaDimensionSpark, Concurrency: 1,
		Credentials: map[string]any{"plan_type": "plus"},
	}
	response := newJSONResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.3-codex-spark\"}}\n\n")
	response.Header.Set("x-codex-turn-state", "opaque-parent")
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{parent.ID: parent, shadow.ID: shadow},
	}}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
	scopedRepo := accountTestDefaultOSRepository(t, repo, parent)
	scopedRepo.accounts[shadow.ID] = shadow
	service := &AccountTestService{accountRepo: scopedRepo, httpUpstream: upstream}
	ctx, recorder := newTestContext()
	require.NoError(t, service.TestAccountConnection(ctx, shadow.ID, "gpt-5.3-codex-spark", "", ""))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "gpt-5.3-codex-spark", info.Get("upstream_model").String())
	require.Equal(t, "Bearer test-token", upstream.requests[0].Header.Get("Authorization"))
	require.NotContains(t, recorder.Body.String(), "opaque-parent")
}

func TestAccountTestResponseInfo_FailedStreamRetainsFailureSemantics(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.failed","response":{"model":"gpt-6-astra-error","error":{"message":"synthetic upstream failure"}}}`,
		`{"type":"error","model":"gpt-6-astra-error","error":{"message":"synthetic upstream failure"}}`,
	} {
		t.Run(gjson.Get(terminal, "type").String(), func(t *testing.T) {
			response := newJSONResponse(http.StatusOK, "data: "+terminal+"\n\n"+
				"data: {\"type\":\"response.completed\"}\n\n")
			response.Header.Set("x-codex-turn-state", "opaque-error")
			service := &AccountTestService{httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{response}}}
			ctx, recorder := newTestContext()
			require.Error(t, service.testOpenAIAccountConnection(ctx, prepareAccountTestCredential(t, service, accountTestResponseInfoOAuth("plus")), "gpt-6-astra", "", ""))
			info := requireAccountTestResponseInfo(t, recorder.Body.String(), "error")
			require.Equal(t, "gpt-6-astra-error", info.Get("upstream_model").String())
			require.NotContains(t, recorder.Body.String(), `"success":true`)
			require.NotContains(t, recorder.Body.String(), "opaque-error")
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
	response.Header.Set("x-codex-turn-state", "opaque-chat")
	upstream := &queuedHTTPUpstream{responses: []*http.Response{response}}
	service := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, prepareAccountTestCredential(t, service, account), "gpt-6-astra", "", ""))
	require.Equal(t, "/v1/chat/completions", upstream.requests[0].URL.Path)
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "returned-chat-model", info.Get("upstream_model").String())
	require.NotContains(t, recorder.Body.String(), "opaque-chat")
}

func TestAccountTestResponseInfo_CompactOAuthReportsResponse(t *testing.T) {
	response := newJSONResponse(http.StatusOK,
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"id\":\"cmp_probe\",\"encrypted_content\":\"synthetic\"}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra-compact\",\"output\":[]}}\n\n")
	response.Header.Set("x-codex-turn-state", "opaque-compact")
	service := &AccountTestService{httpUpstream: &queuedHTTPUpstream{responses: []*http.Response{response}}}
	ctx, recorder := newTestContext()
	require.NoError(t, service.testOpenAIAccountConnection(ctx, prepareAccountTestCredential(t, service, accountTestResponseInfoOAuth("plus")), "gpt-6-astra", "", AccountTestModeCompact))
	info := requireAccountTestResponseInfo(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "gpt-6-astra-compact", info.Get("upstream_model").String())
	require.NotContains(t, recorder.Body.String(), "opaque-compact")
}
