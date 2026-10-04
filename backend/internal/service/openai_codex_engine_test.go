package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func engineAccount() *Account {
	return &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://engine.example/prefix/v1/", "api_key": "engine-key", "model_mapping": map[string]any{"alias": "native", "native": "must-not-map-twice"}},
		Extra:       map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", "openai_passthrough": true, "openai_responses_mode": "force_chat_completions", "openai_compact_supported": false, OpenAIRemoteCompactionV2SupportedExtraKey: false}}
}

func TestCodexEngineModeAndValidation(t *testing.T) {
	a := engineAccount()
	require.True(t, a.IsCodexEngine())
	require.False(t, a.IsOpenAIPassthroughEnabled())
	require.True(t, a.AllowsOpenAICompact())
	for _, capability := range []OpenAIEndpointCapability{OpenAIEndpointCapabilityResponses, OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityRemoteCompactionV2, OpenAIEndpointCapabilityAlphaSearch} {
		require.True(t, a.SupportsOpenAIEndpointCapability(capability))
	}
	require.Equal(t, "native", resolveOpenAIAccountUpstreamModelForRequest(a, "alias", false))
	require.NoError(t, ValidateOpenAIAPIKeyMode(a.Platform, a.Type, a.Extra))
	_, err := buildAccountForCreate(&CreateAccountInput{Platform: a.Platform, Type: a.Type, Credentials: a.Credentials}, a.Extra)
	require.NoError(t, err)
	for _, mode := range []any{"invalid", true, nil} {
		require.Error(t, ValidateOpenAIAPIKeyMode(a.Platform, a.Type, map[string]any{OpenAIAPIKeyModeExtraKey: mode}))
	}
	require.Error(t, ValidateOpenAIAPIKeyMode(a.Platform, AccountTypeOAuth, a.Extra))
	delete(a.Extra, OpenAIAPIKeyModeExtraKey)
	require.False(t, a.IsCodexEngine())
	require.True(t, a.IsOpenAIPassthroughEnabled())
	require.False(t, a.AllowsOpenAICompact())
}

func TestCodexEngineURL(t *testing.T) {
	for _, base := range []string{"https://engine.example/prefix", "https://engine.example/prefix/", "https://engine.example/prefix/v1", "https://engine.example/prefix/v1/"} {
		got, err := codexEngineURL(base, "/v1/responses/compact")
		require.NoError(t, err)
		require.Equal(t, "https://engine.example/prefix/v1/responses/compact", got)
	}
}

func TestCodexEngineModeUpdate(t *testing.T) {
	account := engineAccount()
	account.ID = 721
	account.Extra = nil
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}})
	require.NoError(t, err)
	require.True(t, updated.IsCodexEngine())
	require.Equal(t, "engine-key", updated.GetOpenAIApiKey())
	require.Equal(t, "native", updated.GetMappedModel("alias"))
	_, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "unknown"}})
	require.Error(t, err)
}

func TestCodexEngineStreamDeadlineAndTruncation(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		wire := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
		var body io.ReadCloser = io.NopCloser(strings.NewReader(wire))
		if stalled {
			reader, writer := io.Pipe()
			body = reader
			defer writer.Close()
			go func() { _, _ = writer.Write([]byte(wire)) }()
		}
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}}
		svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 1}}}
		start := time.Now()
		_, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
		require.Error(t, err)
		require.False(t, isCodexEngineResponseError(err))
		require.Less(t, time.Since(start), 3*time.Second)
		require.Equal(t, wire, rec.Body.String(), "never append a synthetic terminal event")
	}
}

func TestCodexEngineMultipartRejectsOversizedPart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "large.png")
	require.NoError(t, err)
	_, err = part.Write(make([]byte, openAIImageMaxUploadPartSize+1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	_, err = codexEngineImagesJSON(body.Bytes(), writer.FormDataContentType())
	require.ErrorContains(t, err, "exceeds")
	parsed := &OpenAIImagesRequest{}
	require.ErrorContains(t, parseOpenAIImagesMultipartRequest(body.Bytes(), writer.FormDataContentType(), parsed), "exceeds")
}

func TestCodexEngineEntrypointsPreserveProtocol(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1/alpha/search", "/v1/images/generations", "/v1/images/edits"} {
		t.Run(path, func(t *testing.T) {
			body := []byte(`{"model":"alias","stream":false,"service_tier":"fast","previous_response_id":"resp_original","input":[{"type":"reasoning","id":"item_original","encrypted_content":"opaque"},{"type":"compaction_trigger"}],"tools":[{"type":"namespace","name":"native","tools":[]}],"unknown":9007199254740993123,"messages":[{"role":"user","content":"hi"}],"images":[{"file_id":"opaque-id"}],"n":2,"quality":"high"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			for _, name := range codexEnginePublicHeaders {
				c.Request.Header.Set(name, "client-public")
			}
			for _, name := range []string{"Authorization", "X-Api-Key", "Cookie", "X-Codex-Owner", "X-Codex-Route-Id", "X-Codex-Continuation-Authorized", "X-Codex-Metering-Request-Id"} {
				c.Request.Header.Set(name, "private")
			}
			response := `{"unknown":9007199254740993123,"usage":{"input_tokens":17,"output_tokens":8,"input_tokens_details":{"cached_tokens":3}},"data":[{"b64_json":"aGVsbG8="}]}`
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "X-Codex-Engine-Token-Estimate": {"estimated-v1"}, "X-Codex-Owner": {"secret"}}, Body: io.NopCloser(strings.NewReader(response))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			a := engineAccount()
			var result *OpenAIForwardResult
			var err error
			switch path {
			case "/v1/responses", "/v1/responses/compact":
				result, err = svc.Forward(context.Background(), c, a, body)
			case "/v1/chat/completions":
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, a, body, "", "")
			case "/v1/messages":
				result, err = svc.ForwardAsAnthropic(context.Background(), c, a, body, "", "")
			case "/v1/messages/count_tokens":
				err = svc.ForwardCountTokensAsAnthropic(context.Background(), c, a, body, "")
			case "/v1/alpha/search":
				result, err = svc.ForwardAlphaSearch(context.Background(), c, a, body)
			default:
				parsed, parseErr := svc.ParseOpenAIImagesRoutingRequest(c, body)
				require.NoError(t, parseErr)
				result, err = svc.ForwardImages(context.Background(), c, a, body, parsed, "")
			}
			require.NoError(t, err)
			require.Equal(t, "https://engine.example/prefix"+path, upstream.lastReq.URL.String())
			require.Equal(t, "Bearer engine-key", upstream.lastReq.Header.Get("Authorization"))
			for _, name := range codexEnginePublicHeaders {
				require.Equal(t, "client-public", upstream.lastReq.Header.Get(name), name)
			}
			for _, name := range []string{"X-Api-Key", "Cookie", "X-Codex-Owner", "X-Codex-Route-Id", "X-Codex-Continuation-Authorized", "X-Codex-Metering-Request-Id"} {
				require.Empty(t, upstream.lastReq.Header.Get(name), name)
			}
			expected := strings.Replace(string(body), `"model":"alias"`, `"model":"native"`, 1)
			expected = strings.Replace(expected, `"service_tier":"fast"`, `"service_tier":"priority"`, 1)
			require.Equal(t, expected, string(upstream.lastBody))
			require.Equal(t, response, rec.Body.String())
			require.Empty(t, rec.Header().Get("X-Codex-Owner"))
			require.Equal(t, "estimated-v1", rec.Header().Get("X-Codex-Engine-Token-Estimate"))
			if result != nil {
				require.Equal(t, 17, result.Usage.InputTokens)
				require.Equal(t, 8, result.Usage.OutputTokens)
				require.Equal(t, 3, result.Usage.CacheReadInputTokens)
			}
		})
	}
}

func TestCodexEngineBusinessErrorsAndSSE(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		failed                  bool
		input, output           int
	}{
		{"native error", "application/json", `{"error":{"code":"native_error","message":"original"}}`, 409, true, 0, 0},
		{"unknown context", "application/json", `{"error":{"code":"context_owner_unknown","type":"gateway_error","message":"Opaque context has no known account owner; restore replayable history"}}`, 409, true, 0, 0},
		{"rate limit", "application/json", `{"error":{"code":"rate_limit","message":"wait"}}`, 429, true, 0, 0},
		{"responses", "text/event-stream", "event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":5}}}\r\n\r\n", 200, false, 4, 5},
		{"chat", "text/event-stream", "data: {\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n", 200, false, 6, 7},
		{"messages", "text/event-stream", "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":9}}}\n\nevent: message_delta\ndata: {\"usage\":{\"output_tokens\":10}}\n\nevent: message_stop\ndata: {}\n\n", 200, false, 9, 10},
		{"failed event", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n", 200, true, 2, 1},
		{"persisted context failure with usage", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\",\"message\":\"{\\\"error\\\":{\\\"code\\\":\\\"unsupported_persisted_item_context\\\",\\\"param\\\":\\\"previous_response_id\\\"},\\\"status\\\":400}\"},\"usage\":{\"input_tokens\":53114,\"output_tokens\":21}}}\n\n", 200, true, 53114, 21},
		{"failed before usage", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\",\"message\":\"Invalid input[28].id: expected rs prefix\"}}}\n\n", 200, true, 0, 0},
		{"zero usage failure", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\"},\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n", 200, true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			wire := tc.body
			if tc.contentType == "text/event-stream" && tc.failed {
				wire += "event: response.completed\ndata: {}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}, "Retry-After": {"42"}, "X-Request-Id": {"req_engine_failure"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			result, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
			require.Equal(t, tc.failed, isCodexEngineResponseError(err))
			if !tc.failed {
				require.NoError(t, err)
			}
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.body, rec.Body.String())
			if tc.failed && tc.input == 0 && tc.output == 0 {
				require.Nil(t, result, "an unmetered rejection must not enter usage accounting")
			} else {
				require.NotNil(t, result)
				require.Equal(t, tc.input, result.Usage.InputTokens)
				require.Equal(t, tc.output, result.Usage.OutputTokens)
			}
			require.Equal(t, "42", rec.Header().Get("Retry-After"))
			require.True(t, CodexEngineResponseWritten(c))
			if tc.failed {
				require.False(t, svc.ReportOpenAIAccountScheduleResult(engineAccount(), "native", false, nil, err))
				expectedStatus := tc.status
				if expectedStatus == 200 {
					expectedStatus = 502
				}
				require.Equal(t, expectedStatus, c.GetInt(OpsUpstreamStatusCodeKey))
				events := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
				require.Len(t, events, 1)
				require.Equal(t, "req_engine_failure", events[0].UpstreamRequestID)
				require.Equal(t, expectedStatus, events[0].UpstreamStatusCode)
				require.NotEmpty(t, events[0].Reason)
				require.NotEmpty(t, events[0].Message)
			}
		})
	}
}

func TestCodexEngineFailureMetadataOmitsGeneratedContentAndCredentials(t *testing.T) {
	wire := `{"type":"response.failed","response":{"id":"resp_failed","error":{"status":403,"code":"too_many_denials","type":"permission_error","message":"denied engine-key https://example.test?access_token=secret"},"output":[{"text":"private generated content"}],"input":"private prompt"}}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"req_fixture"}}, Body: io.NopCloser(strings.NewReader("data: " + wire + "\n\n"))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	result, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	require.Nil(t, result)
	require.ErrorContains(t, err, "(403) [too_many_denials]")
	require.Equal(t, 403, c.GetInt(OpsUpstreamStatusCodeKey))
	events := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, "stream_error", events[0].Kind)
	require.Equal(t, "req_fixture", events[0].UpstreamRequestID)
	for _, private := range []string{"private generated content", "private prompt", "engine-key", "secret"} {
		require.NotContains(t, events[0].Detail, private)
		require.NotContains(t, err.Error(), private)
	}
	require.Equal(t, "data: "+wire+"\n\n", rec.Body.String(), "diagnostics must not alter the upstream response")
}

func TestCodexEngineFailureRetainsPartialImagesAndDiagnosticEvidence(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial image", true: "diagnostic without usage"}[diagnostic], func(t *testing.T) {
			ctx := context.Background()
			prefix := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}}\n\n"
			if diagnostic {
				ctx = withOpenAICandyTest(ctx, &openAICandyTestAttempt{})
				prefix = ""
			}
			wire := prefix + "data: {\"type\":\"response.failed\",\"response\":{\"model\":\"native\",\"error\":{\"code\":\"native_error\",\"message\":\"failed after output\"}}}\n\n"
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			result, err := svc.Forward(ctx, c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
			require.True(t, isCodexEngineResponseError(err))
			require.NotNil(t, result)
			require.Equal(t, "native", result.UpstreamResponseModel)
			require.Zero(t, result.Usage.InputTokens)
			require.Zero(t, result.Usage.OutputTokens)
			if diagnostic {
				require.Zero(t, result.ImageCount)
			} else {
				require.Equal(t, 1, result.ImageCount, "delivered images remain billable even without token usage")
			}
			require.Equal(t, wire, rec.Body.String())
		})
	}
}

func TestCodexEngineMultipartPreservesOrderBytesAndParameters(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, value := range []string{"first", "second"} {
		h := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="image[]"; filename="test.png"`}, "Content-Type": {"image/png"}}
		part, err := w.CreatePart(h)
		require.NoError(t, err)
		_, err = part.Write([]byte(value))
		require.NoError(t, err)
	}
	require.NoError(t, w.WriteField("n", "3"))
	require.NoError(t, w.WriteField("quality", "high"))
	require.NoError(t, w.WriteField("size", "1536x1024"))
	require.NoError(t, w.WriteField("unknown", "kept"))
	require.NoError(t, w.Close())
	result, err := codexEngineImagesJSON(body.Bytes(), w.FormDataContentType())
	require.NoError(t, err)
	require.Equal(t, "data:image/png;base64,Zmlyc3Q=", gjson.GetBytes(result, "images.0.image_url").String())
	require.Equal(t, "data:image/png;base64,c2Vjb25k", gjson.GetBytes(result, "images.1.image_url").String())
	require.Equal(t, int64(3), gjson.GetBytes(result, "n").Int())
	require.Equal(t, "high", gjson.GetBytes(result, "quality").String())
	require.Equal(t, "1536x1024", gjson.GetBytes(result, "size").String())
	require.Equal(t, "kept", gjson.GetBytes(result, "unknown").String())
}
