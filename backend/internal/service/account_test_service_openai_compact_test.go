package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// compactProbeSSESuccessBody 是原生 v2 压缩成功的最小 SSE 形态：
// output_item.done 携带 compaction item + response.completed。
const compactProbeSSESuccessBody = "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"id\":\"cmp_probe\",\"encrypted_content\":\"blob\"}}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_probe\",\"output\":[]}}\n\n"

func TestAccountTestService_TestAccountConnection_OpenAICompactOAuthSuccessPersistsSupport(t *testing.T) {
	gin.SetMode(gin.TestMode)

	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":               "oauth-token",
			"chatgpt_account_id":         "chatgpt-acc",
			"chatgpt_account_is_fedramp": true,
		},
		Extra: map[string]any{openAIPinnedInstallationIDKey: "11111111-2222-4333-8444-555555555555"},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-probe"}},
		Body:       io.NopCloser(strings.NewReader(compactProbeSSESuccessBody)),
	}}
	svc := &AccountTestService{
		accountRepo:  accountTestDefaultOSRepository(t, repo, &account),
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", bytes.NewReader(nil))
	c.Request.Header.Set(codexInstallationIDKey, "client-installation")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested","turn":1}`)

	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact)
	require.NoError(t, err)

	// 原生 v2：探测普通 /responses 线，不再打已下线的 /responses/compact。
	require.Equal(t, chatgptCodexAPIURL, upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Contains(t, upstream.lastReq.Header.Get("x-codex-beta-features"), "remote_compaction_v2")
	require.Equal(t, codexCLIVersion, upstream.lastReq.Header.Get("Version"))
	probeSessionID := upstream.lastReq.Header.Get("session-id")
	require.True(t, ValidateFingerprintObservationUUIDv7(probeSessionID))
	require.Equal(t, probeSessionID, upstream.lastReq.Header.Get("thread-id"))
	require.Empty(t, upstream.lastReq.Header.Get("session_id"))
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, codexCLIUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "chatgpt-acc", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("x-openai-fedramp"))
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.True(t, gjson.GetBytes(upstream.lastBody, "client_metadata").Exists(), "native v2 uses the regular Responses projection")
	require.Empty(t, upstream.lastReq.Header.Get(codexInstallationIDKey))
	require.Equal(t, "11111111-2222-4333-8444-555555555555", extractInstallationIDFromTurnMetadata(upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader)))
	inputItems := gjson.GetBytes(upstream.lastBody, "input").Array()
	require.NotEmpty(t, inputItems)
	require.Equal(t, "compaction_trigger", inputItems[len(inputItems)-1].Get("type").String())

	updates := <-updateCalls
	require.Equal(t, true, updates[OpenAIRemoteCompactionV2SupportedExtraKey])
	require.Equal(t, http.StatusOK, updates[OpenAIRemoteCompactionV2LastStatusExtraKey])
	require.NotContains(t, updates, "openai_compact_supported")
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

func TestAccountTestService_TestAccountConnection_OpenAICompactOAuth404PreservesSupported(t *testing.T) {
	gin.SetMode(gin.TestMode)

	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          2,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`404 page not found`)),
	}}
	svc := &AccountTestService{
		accountRepo:  accountTestDefaultOSRepository(t, repo, &account),
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/2/test", bytes.NewReader(nil))

	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact)
	require.Error(t, err)

	updates := <-updateCalls
	require.NotContains(t, updates, OpenAIRemoteCompactionV2SupportedExtraKey)
	require.Equal(t, http.StatusNotFound, updates[OpenAIRemoteCompactionV2LastStatusExtraKey])
	require.Contains(t, rec.Body.String(), `"type":"error"`)
}

func TestAccountTestService_TestAccountConnection_OpenAICompactAPIKeyUsesNativeResponsesPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          3,
		Name:        "openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://example.com/v1",
			// post-#5641：compact_model_mapping 仅作用于 legacy /responses/compact，
			// 原生 v2 探测不应用它。
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(compactProbeSSESuccessBody)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/3/test", bytes.NewReader(nil))

	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact)
	require.NoError(t, err)

	require.Equal(t, "https://example.com/v1/responses", upstream.lastReq.URL.String())
	requireOpenAICodexProbeHeaders(t, upstream.lastReq.Header)
	require.Contains(t, upstream.lastReq.Header.Get("x-codex-beta-features"), "remote_compaction_v2")
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String(),
		"原生 v2 探测不应用 compact_model_mapping")
	updates := <-updateCalls
	require.Equal(t, true, updates[OpenAIRemoteCompactionV2SupportedExtraKey])
	require.NotContains(t, updates, "openai_compact_supported")
}

func TestAccountTestService_TestAccountConnection_OpenAICompactAPIKeyDefaultBaseURLUsesResponsesPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          4,
		Name:        "openai-apikey-default",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(compactProbeSSESuccessBody)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/4/test", bytes.NewReader(nil))

	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact)
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
	<-updateCalls
}

func TestAccountTestService_TestAccountConnection_OpenAICompact2xxWithoutItemMarksUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)

	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          5,
		Name:        "openai-oauth-no-item",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	// 200 但流里没有 compaction item：链路吞掉了 compaction_trigger 的形态
	//（#5478 的 "got 0 items"），必须判定为不支持。
	noItemBody := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"output\":[]}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(noItemBody)),
	}}
	svc := &AccountTestService{
		accountRepo:  accountTestDefaultOSRepository(t, repo, &account),
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/5/test", bytes.NewReader(nil))

	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact)
	require.Error(t, err)

	updates := <-updateCalls
	require.Equal(t, false, updates[OpenAIRemoteCompactionV2SupportedExtraKey])
	require.NotContains(t, updates, "openai_compact_supported")
	require.Contains(t, rec.Body.String(), `"type":"error"`)
}

func TestAccountTestService_OpenAICompactExecutionFailurePreservesCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	failed := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"native_error\",\"message\":\"Native compaction failed: checkpoint unavailable\"}}}\n\n"
	item := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\n"
	tests := []struct {
		name     string
		body     string
		readErr  bool
		want     string
		wantCode string
	}{
		{name: "engine_failure", body: "data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n" + failed, want: "Native compaction failed: checkpoint unavailable", wantCode: "native_error"},
		{name: "failure_after_compaction_item", body: item + failed, want: "Native compaction failed: checkpoint unavailable", wantCode: "native_error"},
		{name: "named_error_event", body: "event: error\ndata: {\"code\":\"worker_error\",\"message\":\"Worker disconnected\"}\n\n", want: "Worker disconnected", wantCode: "worker_error"},
		{name: "incomplete", body: "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", want: "max_output_tokens"},
		{name: "json_failure", body: `{"status":"failed","error":{"code":"compaction_failed","message":"Native checkpoint missing"},"output":[]}`, want: "Native checkpoint missing", wantCode: "compaction_failed"},
		{name: "stream_without_terminal", body: item, want: "without a completed response"},
		{name: "read_failure", body: item, readErr: true, want: "unexpected EOF"},
		{name: "oversized", body: compactProbeSSESuccessBody + strings.Repeat(" ", 2<<20), want: "exceeds the 2 MiB limit"},
	}
	for _, mode := range []string{"generic", "codex_engine"} {
		for _, test := range tests {
			for _, prior := range []any{nil, true, false} {
				name := fmt.Sprintf("%s/%s/prior_%v", mode, test.name, prior)
				t.Run(name, func(t *testing.T) {
					updatesCh := make(chan map[string]any, 1)
					account := Account{
						ID: 6, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
						Status: StatusActive, Schedulable: true, Concurrency: 1,
						Credentials: map[string]any{"api_key": "engine-test-key", "base_url": "https://engine.example/v1"},
						Extra:       map[string]any{"openai_api_key_mode": mode},
					}
					if prior != nil {
						account.Extra[OpenAIRemoteCompactionV2SupportedExtraKey] = prior
					}
					repo := &snapshotUpdateAccountRepo{
						stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
						updateExtraCalls:      updatesCh,
					}
					var body io.Reader = strings.NewReader(test.body)
					if test.readErr {
						body = io.MultiReader(body, iotest.ErrReader(io.ErrUnexpectedEOF))
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(body),
					}}
					svc := &AccountTestService{
						accountRepo: repo, httpUpstream: upstream,
						cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/6/test", nil)

					err := svc.TestAccountConnection(c, account.ID, "gpt-6-astra", "", AccountTestModeCompact)
					require.ErrorContains(t, err, test.want)
					select {
					case updates := <-updatesCh:
						require.NotContains(t, updates, OpenAIRemoteCompactionV2SupportedExtraKey)
						require.Equal(t, http.StatusOK, updates[OpenAIRemoteCompactionV2LastStatusExtraKey])
						require.Contains(t, updates[OpenAIRemoteCompactionV2LastErrorExtraKey], test.want)
						require.Contains(t, updates[OpenAIRemoteCompactionV2LastErrorExtraKey], test.wantCode)
					default:
						t.Fatal("missing probe diagnostic update")
					}
					require.Contains(t, rec.Body.String(), test.want)
					require.Contains(t, rec.Body.String(), test.wantCode)
					require.NotContains(t, rec.Body.String(), "unsupported on this chain")
					require.NotContains(t, rec.Body.String(), `"success":true`)
				})
			}
		}
	}
}
