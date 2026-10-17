package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type engineCyberResponseBody struct {
	io.Reader
	onClose func()
}

func (b *engineCyberResponseBody) Close() error {
	b.onClose()
	return nil
}

func TestCodexEngineCyberPolicyContract(t *testing.T) {
	const message = "Request rejected by upstream cyber-security policy"
	for _, tc := range []struct {
		name, contentType, wire string
		status, semanticStatus  int
		input, output, cached   int
		cyber                   bool
	}{
		{
			name: "HTTP rejection", contentType: "application/json", status: 400, semanticStatus: 400, cyber: true,
			wire: `{"error":{"code":"cyber_policy","type":"invalid_request_error","message":"` + message + `"}}`,
		},
		{
			name: "intermediary 502 still recognizes exact cyber code", contentType: "application/json", status: 502, semanticStatus: 502, cyber: true,
			wire: `{"error":{"code":"cyber_policy","type":"gateway_error","message":"` + message + `"}}`,
		},
		{
			name: "failed SSE has semantic 400 and actual usage", contentType: "text/event-stream", status: 200, semanticStatus: 400, cyber: true,
			input: 19, output: 3, cached: 11,
			wire: "event: response.failed\ndata: " + `{"type":"response.failed","response":{"id":"resp_engine_cyber","status":"failed","error":{"code":"cyber_policy","type":"invalid_request_error","status_code":400,"message":"` + message + `"},"usage":{"input_tokens":19,"output_tokens":3,"input_tokens_details":{"cached_tokens":11}}}}` + "\n\n",
		},
		{
			name: "bare error waits for authoritative terminal usage", contentType: "text/event-stream", status: 200, semanticStatus: 400, cyber: true,
			input: 19, output: 3, cached: 11,
			wire: "event: error\ndata: " + `{"type":"error","error":{"code":"cyber_policy","type":"invalid_request_error","status_code":400,"message":"` + message + `"}}` + "\n\n" +
				"event: response.failed\ndata: " + `{"type":"response.failed","response":{"id":"resp_engine_cyber","status":"failed","usage":{"input_tokens":19,"output_tokens":3,"input_tokens_details":{"cached_tokens":11}}}}` + "\n\n",
		},
		{
			name: "bare error EOF", contentType: "text/event-stream", status: 200, semanticStatus: 400, cyber: true,
			wire: "event: error\ndata: " + `{"type":"error","error":{"code":"cyber_policy","type":"invalid_request_error","status_code":400,"message":"` + message + `"}}` + "\n\n",
		},
		{
			name: "message cannot promote native error", contentType: "application/json", status: 502, semanticStatus: 502,
			wire: `{"error":{"code":"native_error","message":"cyber_policy: {\"error\":{\"code\":\"cyber_policy\"}}"}}`,
		},
		{
			name: "message cannot promote streamed native error", contentType: "text/event-stream", status: 200, semanticStatus: 502,
			wire: "event: response.failed\ndata: " + `{"type":"response.failed","response":{"error":{"code":"native_error","message":"cyber_policy"}}}` + "\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			closed := false
			body := &engineCyberResponseBody{Reader: strings.NewReader(tc.wire), onClose: func() {
				closed = true
				require.Equal(t, tc.wire, rec.Body.String(), "deliver the original error before closing its upstream body")
			}}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status,
				Header: http.Header{"Content-Type": {tc.contentType}, "X-Request-Id": {"req_engine_cyber"}}, Body: body}}
			healthSettings, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 1, CooldownMinutes: 5})
			require.NoError(t, err)
			healthCache := &openAIAPIKeyHealthCacheStub{tripped: true}
			healthRepo := &openAIAPIKeyHealthAccountRepo{}
			healthBlocker := &openAIAPIKeyHealthRuntimeBlocker{}
			rateLimits := NewRateLimitService(healthRepo, nil, &config.Config{}, nil, healthCache)
			rateLimits.SetSettingService(NewSettingService(&openAIAPIKeyHealthSettingRepo{value: string(healthSettings)}, &config.Config{}))
			rateLimits.SetOpenAIAPIKeyHealthCache(healthCache)
			rateLimits.SetAccountRuntimeBlocker(healthBlocker)
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimits}
			account := engineAccount()
			account.ID, account.Schedulable = 42, true
			account.Credentials["pool_mode"] = true
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"alias","stream":true}`))
			var failure *codexEngineResponseError
			require.ErrorAs(t, err, &failure)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, tc.semanticStatus, failure.status)
			require.True(t, closed)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.wire, rec.Body.String())
			require.Equal(t, "req_engine_cyber", rec.Header().Get("X-Request-Id"))
			require.Len(t, upstream.requests, 1)
			if tc.input != 0 {
				require.NotNil(t, result, "retain complete authoritative usage for handler-side accounting")
				require.Equal(t, tc.input, result.Usage.InputTokens)
				require.Equal(t, tc.output, result.Usage.OutputTokens)
				require.Equal(t, tc.cached, result.Usage.CacheReadInputTokens)
			} else {
				require.Nil(t, result)
			}
			mark := GetOpsCyberPolicy(c)
			if tc.cyber {
				require.NotNil(t, mark)
				require.Equal(t, "cyber_policy", mark.Code)
				require.Equal(t, message, mark.Message)
				require.Equal(t, tc.semanticStatus, mark.UpstreamStatus)
				require.Equal(t, tc.input, mark.UpstreamInTok)
				require.Equal(t, tc.output, mark.UpstreamOutTok)
				require.Equal(t, "cyber_policy", gjson.Get(mark.Body, "error.code").String())
				require.False(t, gjson.Get(mark.Body, "response").Exists(), "diagnostics retain only the error envelope")
			} else {
				require.Nil(t, mark)
			}
			events, ok := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, "req_engine_cyber", events[0].UpstreamRequestID)
			require.Equal(t, tc.semanticStatus, events[0].UpstreamStatusCode)
			require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "native", false, nil, err))
			require.False(t, svc.ObserveOpenAIAccountHealthFailure(context.Background(), account, err))
			require.Zero(t, healthCache.recordCalls)
			require.Zero(t, healthCache.setCalls)
			require.Zero(t, healthRepo.setCalls)
			require.Zero(t, healthBlocker.calls)
			require.False(t, svc.isCodexEngineDeploymentUnavailable(account, time.Now()))
			require.True(t, account.Schedulable)
			require.Nil(t, account.TempUnschedulableUntil)
		})
	}
}

func TestCodexEngineCyberTerminalClosesBodyWithoutWaitingForEOF(t *testing.T) {
	const terminal = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"cyber_policy\",\"status_code\":400,\"message\":\"blocked\"}}}\n\n"
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	go func() { _, _ = io.WriteString(writer, terminal) }()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
		done <- err
	}()
	select {
	case err := <-done:
		require.True(t, isCodexEngineResponseError(err))
	case <-time.After(2 * time.Second):
		_ = writer.Close()
		<-done
		t.Fatal("cyber terminal did not release the stream while EOF remained pending")
	}
	require.Equal(t, terminal, rec.Body.String())
	require.NotNil(t, GetOpsCyberPolicy(c))
	require.Len(t, upstream.requests, 1)
	_, err := writer.Write([]byte("later event"))
	require.ErrorIs(t, err, io.ErrClosedPipe)
}
