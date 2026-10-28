package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCodex0165StreamOptionsTransportContract(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{"absent", "", ""},
		{"supported", `{"reasoning_summary_delivery":"sequential_cutoff"}`, "sequential_cutoff"},
		{"mixed", `{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true,"future":{}}`, "sequential_cutoff"},
		{"usage only", `{"include_usage":true}`, ""},
		{"empty", `{}`, ""},
		{"unknown", `{"reasoning_summary_delivery":"concurrent"}`, ""},
		{"wrong value type", `{"reasoning_summary_delivery":{}}`, ""},
		{"null", `null`, ""},
		{"array", `[]`, ""},
		{"string", `"sequential_cutoff"`, ""},
		{"boolean", `true`, ""},
		{"number", `1`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.1-sol","input":[],"store":false,"stream":true}`)
			if test.raw != "" {
				var err error
				body, err = sjson.SetRawBytes(body, "stream_options", []byte(test.raw))
				require.NoError(t, err)
			}
			for _, compact := range []bool{false, true} {
				want := test.want
				if compact {
					want = ""
				}
				check := func(out []byte) {
					t.Helper()
					options := gjson.GetBytes(out, "stream_options")
					require.Equal(t, want != "", options.Exists(), "compact=%v body=%s", compact, out)
					if want != "" {
						require.Equal(t, want, options.Get("reasoning_summary_delivery").String())
						require.Len(t, options.Map(), 1)
					}
				}
				var request map[string]any
				require.NoError(t, json.Unmarshal(body, &request))
				applyCodexOAuthTransform(request, true, compact)
				encoded, err := json.Marshal(request)
				require.NoError(t, err)
				check(encoded)
				out, _, err := normalizeOpenAIPassthroughOAuthBody(body, compact)
				require.NoError(t, err)
				check(out)
				for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
					out, _, err = normalizeOpenAIResponsesCompatibilityBodyWithOptions(body,
						&Account{Platform: PlatformOpenAI, Type: accountType}, openAIResponsesCompatibilityOptions{Compact: compact})
					require.NoError(t, err)
					check(out)
				}
			}
			apiBody, _, err := normalizeOpenAIResponsesCompatibilityBodyWithOptions(body,
				&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, openAIResponsesCompatibilityOptions{})
			require.NoError(t, err)
			require.Equal(t, test.raw, gjson.GetBytes(apiBody, "stream_options").Raw)
		})
	}
}

func TestCodex0165StreamOptionsFinalizerPreservesAPIKeyContract(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-6.1-sol","instructions":"client instructions","stream_options":{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true}}`)
	svc := &OpenAIGatewayService{}
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		account := &Account{Platform: PlatformOpenAI, Type: accountType}
		request := httptest.NewRequest(http.MethodPost, "https://example.test/v1/responses", nil)
		httpBody, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, request, body,
			OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6.1-sol"})
		require.NoError(t, err)
		wsBody, err := svc.projectOpenAIOAuthWSFrame(nil, account, OpenAIOAuthIdentityPlan{}, body)
		require.NoError(t, err)
		for _, out := range [][]byte{httpBody, wsBody} {
			require.Equal(t, "sequential_cutoff", gjson.GetBytes(out, "stream_options.reasoning_summary_delivery").String(), accountType)
			require.Equal(t, accountType == AccountTypeAPIKey, gjson.GetBytes(out, "stream_options.include_usage").Exists(), accountType)
		}
	}
}

func TestCodex0165NativePromptCacheAbsenceSurvivesProjectionAndFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "unrelated-client/1.0")
	c.Set("api_key", &APIKey{ID: 165})
	body := []byte(`{"model":"gpt-6.1-sol","client_metadata":{"session_id":"native-root","thread_id":"native-child"},"input":[]}`)
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	require.Equal(t, OpenAICodexPromptCachePreserveAbsent, capture.PromptCacheKey.AbsencePolicy)
	SetOpenAIOAuthIdentityCapture(c, capture)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "codex-0165-cache-test"}}}
	var previous *OpenAIOAuthIdentityPlan
	for _, account := range []*Account{
		{ID: 16501, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 16502, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 16510, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{ID: 16511, Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
	} {
		svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
		for _, mode := range []OpenAIOAuthIdentityProjectionMode{
			OpenAIOAuthIdentityProjectionRegular,
			OpenAIOAuthIdentityProjectionPassthrough,
			OpenAIOAuthIdentityProjectionCompact,
		} {
			options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, ProjectionMode: mode, InstallationPolicy: OpenAIOAuthInstallationPreserve}
			plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, previous)
			require.NoError(t, err)
			require.Equal(t, OpenAICodexPromptCacheKeyOmit, plan.PromptCacheKey.Action)
			require.NotEmpty(t, plan.TurnIdentity.SessionID)
			require.NotEmpty(t, plan.TurnIdentity.ThreadID)
			// Simulate intermediate compatibility and previous-attempt mutations.
			mutated, err := sjson.SetBytes(body, "prompt_cache_key", "intermediate-cache-key")
			require.NoError(t, err)
			headers := http.Header{}
			out, err := ApplyOpenAIOAuthIdentityPlan(headers, mutated, plan)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(out, "prompt_cache_key").Exists())
			require.Equal(t, plan.TurnIdentity.SessionID, headers.Get("session-id"))
			require.Equal(t, plan.TurnIdentity.ThreadID, headers.Get("thread-id"))
			retry, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, &plan)
			require.NoError(t, err)
			require.Equal(t, plan.PromptCacheKey, retry.PromptCacheKey)
			require.True(t, openAIOAuthIdentityCapturesEqual(capture, retry.Capture))
			previous = &plan
		}
	}
}

func TestCodex0165PromptCacheMissingAndExplicitInvalidStayDistinct(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "codex-0165-invalid-test"}}}
	account := &Account{ID: 16503, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, raw := range []string{"null", `""`, `"  "`, "7", "false", "{}", "[]"} {
		body := []byte(`{"client_metadata":{"session_id":"invalid-root"},"prompt_cache_key":` + raw + `}`)
		capture, plan := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionRegular, 165)
		require.True(t, capture.PromptCacheKey.Present, raw)
		require.Equal(t, OpenAICodexPromptCacheKeyInvalid, capture.PromptCacheKey.Kind, raw)
		require.Equal(t, OpenAICodexPromptCacheKeySet, plan.PromptCacheKey.Action, raw)
		require.Equal(t, plan.TurnIdentity.SessionID, plan.PromptCacheKey.Value, raw)
	}
	for _, capture := range []OpenAIOAuthIdentityCapture{
		CaptureOpenAIOAuthIdentityForCompatTurn(nil, []byte(`{"messages":[]}`), "compat-root"),
		CaptureOpenAIOAuthIdentityForSyntheticTurn(nil, []byte(`{"input":[]}`), "probe-root"),
		CaptureOpenAIOAuthIdentityForAlphaSearch(nil, []byte(`{}`), "alpha-root"),
	} {
		require.Equal(t, OpenAICodexPromptCacheSynthesizeSession, capture.PromptCacheKey.AbsencePolicy)
		plan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), nil, account, capture, OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular, InstallationPolicy: OpenAIOAuthInstallationPreserve,
		})
		require.NoError(t, err)
		require.Equal(t, OpenAICodexPromptCacheKeySet, plan.PromptCacheKey.Action)
		require.Equal(t, plan.TurnIdentity.SessionID, plan.PromptCacheKey.Value)
	}
}

func TestCodex0165PromptCachePolicyInvalidatesCachedPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(`{"client_metadata":{"session_id":"policy-root"}}`)
	account := &Account{ID: 16504, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{accountRepo: newAuthorizedOpenAIOAuthTestRepo(account)}
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular, InstallationPolicy: OpenAIOAuthInstallationPreserve}
	before, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, nil)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexPromptCacheKeyOmit, before.PromptCacheKey.Action)
	changed := cloneOpenAIOAuthIdentityCapture(capture)
	changed.PromptCacheKey.AbsencePolicy = OpenAICodexPromptCacheSynthesizeSession
	require.False(t, openAIOAuthIdentityCapturesEqual(capture, changed))
	after, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, changed, options, &before)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexPromptCacheKeySet, after.PromptCacheKey.Action)
	require.Equal(t, before.TurnIdentity, after.TurnIdentity)
}

func TestCodex0165WSFramePromptCacheOverrideAbsentOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 165})
	account := &Account{ID: 16505, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), cfg: &config.Config{JWT: config.JWTConfig{Secret: "codex-0165-ws-test"}}}
	options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionPassthrough, InstallationPolicy: OpenAIOAuthInstallationPreserve}
	initial := CaptureOpenAIOAuthIdentity(c, []byte(`{"client_metadata":{"session_id":"ws-native-root","thread_id":"ws-native-child"}}`), "")
	current, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, initial, options, nil)
	require.NoError(t, err)
	stable := current.TurnIdentity
	digest := openAIWSOutboundIdentityPlanDigest(http.Header{}, current)
	firstOverride := ""
	for _, key := range []string{"override-scope", "", "override-scope"} {
		frame := []byte(`{"type":"response.create","model":"gpt-6.1-sol","input":[],"stream_options":{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true}}`)
		if key != "" {
			frame, err = sjson.SetBytes(frame, "prompt_cache_key", key)
			require.NoError(t, err)
		}
		capture := captureOpenAIWSFrameIdentity(frame, &current)
		require.Equal(t, initial.PromptCacheKey.AbsencePolicy, capture.PromptCacheKey.AbsencePolicy)
		plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, &current)
		require.NoError(t, err)
		require.Equal(t, stable, plan.TurnIdentity)
		require.Equal(t, digest, openAIWSOutboundIdentityPlanDigest(http.Header{}, plan))
		// Exercise the same final projection used for WS frames and HTTP bridges.
		out, err := svc.projectOpenAIOAuthWSFrame(c, account, plan, frame)
		require.NoError(t, err)
		require.Equal(t, "sequential_cutoff", gjson.GetBytes(out, "stream_options.reasoning_summary_delivery").String())
		require.False(t, gjson.GetBytes(out, "stream_options.include_usage").Exists())
		if key == "" {
			require.Equal(t, OpenAICodexPromptCacheKeyOmit, plan.PromptCacheKey.Action)
			require.False(t, gjson.GetBytes(out, "prompt_cache_key").Exists())
		} else {
			require.Equal(t, OpenAICodexPromptCacheKeySet, plan.PromptCacheKey.Action)
			require.Equal(t, OpenAICodexPromptCacheKeyOverride, capture.PromptCacheKey.Kind)
			require.Equal(t, plan.PromptCacheKey.Value, gjson.GetBytes(out, "prompt_cache_key").String())
			if firstOverride == "" {
				firstOverride = plan.PromptCacheKey.Value
			} else {
				require.Equal(t, firstOverride, plan.PromptCacheKey.Value)
			}
		}
		current = plan
	}
	compat := current
	compat.Capture.PromptCacheKey.AbsencePolicy = OpenAICodexPromptCacheSynthesizeSession
	for _, frame := range []string{`{"type":"response.create"}`, `{"type":"response.create","client_metadata":{"session_id":"other-root"}}`} {
		next := captureOpenAIWSFrameIdentity([]byte(frame), &compat)
		require.Equal(t, OpenAICodexPromptCacheSynthesizeSession, next.PromptCacheKey.AbsencePolicy)
		require.False(t, next.PromptCacheKey.Present)
		require.Empty(t, next.PromptCacheKey.Value)
	}
}

func TestCodex0165ResponsesShapeChatWireContract(t *testing.T) {
	for _, oauth := range []bool{true, false} {
		t.Run(map[bool]string{true: "OAuth", false: "API key unchanged"}[oauth], func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.1-sol","stream":true,"client_metadata":{"session_id":"responses-shape-root"},"input":[{"role":"user","content":"hi"}],"stream_options":{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true}}`)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
			}}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			svc.settingService = nil
			account := newOpenAIIdentityPathAPIKeyAccount(16507)
			if oauth {
				account = newOpenAIIdentityPathOAuthAccount(16506)
				svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
			}
			c, _ := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 165)
			_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-6.1-sol")
			require.Error(t, err)
			require.NotNil(t, upstream.lastReq)
			if oauth {
				require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
				require.NotEmpty(t, upstream.lastReq.Header.Get("session-id"))
				require.Equal(t, "sequential_cutoff", gjson.GetBytes(upstream.lastBody, "stream_options.reasoning_summary_delivery").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Exists())
			} else {
				require.False(t, gjson.GetBytes(upstream.lastBody, "stream_options").Exists())
			}
		})
	}
}

func TestCodex0165WSHTTPBridgePreservesNativeAbsenceAndStreamOptions(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-6.1-sol","stream":true,"client_metadata":{"session_id":"bridge-native-root"},"input":[],"stream_options":{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 165)
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	SetOpenAIOAuthIdentityCapture(c, capture)
	response := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_0165\",\"model\":\"gpt-6.1-sol\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")),
		}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{response(), response()}}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	svc.settingService = nil
	var previous *OpenAIOAuthIdentityPlan
	for _, id := range []int64{16508, 16509} {
		account := newOpenAIIdentityPathOAuthAccount(id)
		svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
		plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture,
			OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionPassthrough, InstallationPolicy: OpenAIOAuthInstallationPreserve}, previous)
		require.NoError(t, err)
		require.Equal(t, OpenAICodexPromptCacheKeyOmit, plan.PromptCacheKey.Action)
		// A replay body may carry an intermediate key; the original capture
		// remains authoritative when the bridge materializes another owner.
		payload, err := sjson.SetBytes(body, "prompt_cache_key", "intermediate-cache")
		require.NoError(t, err)
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "oauth-token", payload, len(payload),
			"gpt-6.1-sol", "", "", "", "", 1, func([]byte) error { return nil }, &plan)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
		require.Equal(t, "sequential_cutoff", gjson.GetBytes(upstream.lastBody, "stream_options.reasoning_summary_delivery").String())
		require.False(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Exists())
		require.Equal(t, plan.TurnIdentity.SessionID, upstream.lastReq.Header.Get("session-id"))
		previous = &plan
	}
}

func TestCodex0165SynchronousFinalWirePromptCacheUsesFinalIdentity(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, test := range []struct {
				name     string
				key      string
				compat   bool
				omit     bool
				override bool
			}{
				{name: "native absent", omit: true},
				{name: "explicit default", key: `"sync-logical-root"`},
				{name: "explicit null", key: "null"},
				{name: "explicit override", key: `"sync-cache-override"`, override: true},
				{name: "compat absent", compat: true},
			} {
				t.Run(fmt.Sprintf("passthrough=%v/compact=%v/%s", passthrough, compact, test.name), func(t *testing.T) {
					original := []byte(`{"model":"gpt-6.1-sol","stream":false,"instructions":"client instructions","client_metadata":{"session_id":"sync-logical-root"},"input":[]}`)
					if test.key != "" {
						var err error
						original, err = sjson.SetRawBytes(original, "prompt_cache_key", []byte(test.key))
						require.NoError(t, err)
					}
					path := "/v1/responses"
					if compact {
						path += "/compact"
					}
					c, _ := newOpenAIIdentityPathContext(t, path, original, 165)
					capture := CaptureOpenAIOAuthIdentity(c, original, "")
					if test.compat {
						capture = CaptureOpenAIOAuthIdentityForCompatTurn(c, original, "")
					}
					SetOpenAIOAuthIdentityCapture(c, capture)
					svc, _ := newOpenAIIdentityPathService(t, true, nil)
					svc.settingService = nil
					account := newOpenAIIdentityPathOAuthAccount(16512)
					svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
					payload := original
					if test.omit {
						var err error
						payload, err = sjson.SetBytes(payload, "prompt_cache_key", "intermediate-key")
						require.NoError(t, err)
					}
					var firstKey, firstThread string
					for attempt := 0; attempt < 2; attempt++ {
						var req *http.Request
						var err error
						if passthrough {
							req, err = svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, payload, "oauth-token")
						} else {
							req, err = svc.buildUpstreamRequest(c.Request.Context(), c, account, payload, "oauth-token", false, "", false)
						}
						require.NoError(t, err)
						out := readOpenAIIdentityPathRequestBody(t, req)
						plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
						require.True(t, ok)
						require.True(t, plan.Synchronous)
						require.Equal(t, req.Header.Get("session-id"), plan.TurnIdentity.SessionID)
						key := gjson.GetBytes(out, "prompt_cache_key")
						if test.omit {
							require.False(t, key.Exists())
						} else if test.override {
							require.True(t, strings.HasPrefix(key.String(), "pc_"))
							require.NotEqual(t, req.Header.Get("session-id"), key.String())
						} else {
							require.Equal(t, req.Header.Get("session-id"), key.String())
						}
						if attempt == 0 {
							firstKey, firstThread = key.String(), req.Header.Get("thread-id")
						} else {
							require.Equal(t, firstKey, key.String())
							require.Equal(t, firstThread, req.Header.Get("thread-id"))
						}
					}
				})
			}
		}
	}
}

func TestCodex0165DailyFallbackEnrichesFrozenCaptureWithoutRecapture(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%v", passthrough), func(t *testing.T) {
			original := []byte(`{"model":"gpt-6.1-sol","stream":true,"instructions":"client instructions","input":[]}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", original, 165)
			capture := CaptureOpenAIOAuthIdentity(c, original, "")
			require.Empty(t, capture.Logical.SessionKey)
			SetOpenAIOAuthIdentityCapture(c, capture)
			setOpenAIClientRequestedStream(c, true)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
			}}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			svc.settingService = NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
				SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
				SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
				SettingKeyEnableOpenAIOAuthDailySessionRotation:     "true",
			}}, nil)
			account := newOpenAIIdentityPathOAuthAccount(16513)
			svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
			mutated, err := sjson.SetBytes(original, "prompt_cache_key", "intermediate-key")
			require.NoError(t, err)
			var out []byte
			if passthrough {
				req, buildErr := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, mutated, "oauth-token")
				require.NoError(t, buildErr)
				out = readOpenAIIdentityPathRequestBody(t, req)
			} else {
				_, forwardErr := svc.Forward(c.Request.Context(), c, account, mutated)
				require.Error(t, forwardErr)
				require.NotNil(t, upstream.lastReq)
				out = upstream.lastBody
			}
			after, ok := OpenAIOAuthIdentityCaptureFromContext(c)
			require.True(t, ok)
			require.NotEmpty(t, after.Logical.SessionKey)
			require.Equal(t, capture.PromptCacheKey, after.PromptCacheKey)
			require.Equal(t, capture.RequestTurn, after.RequestTurn)
			require.Equal(t, capture.ContextWindowIDCandidate, after.ContextWindowIDCandidate)
			require.False(t, gjson.GetBytes(out, "prompt_cache_key").Exists())
		})
	}
}
