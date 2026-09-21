package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexTelemetryHTTPRecorder struct {
	finished []CodexTelemetryResult
	retried  []CodexTelemetryResult
}

func (r *codexTelemetryHTTPRecorder) Finish(result CodexTelemetryResult) {
	r.finished = append(r.finished, result)
}

func (r *codexTelemetryHTTPRecorder) Retry(result CodexTelemetryResult) {
	r.retried = append(r.retried, result)
}

type codexTelemetryHTTPChunkReader struct {
	reader io.Reader
	limit  int
}

func (r codexTelemetryHTTPChunkReader) Read(p []byte) (int, error) {
	if len(p) > r.limit {
		p = p[:r.limit]
	}
	return r.reader.Read(p)
}

func TestCodexTelemetryHTTPResponseTransparentCompletion(t *testing.T) {
	terminal := `{"id":"resp_actual","status":"completed","output":[{"content":[{"text":"private response text"}]}],"usage":{"input_tokens":123,"output_tokens":17,"input_tokens_details":{"cached_tokens":41},"output_tokens_details":{"reasoning_tokens":9}}}`
	for _, stream := range []bool{false, true} {
		name, contentType, body := "json", "application/json", terminal
		if stream {
			name, contentType = "sse", "text/event-stream"
			body = "event: response.created\r\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_actual\"}}\r\n\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"secret\"}\n\ndata: {\"type\":\"response.completed\",\"response\":" + terminal + "}\n\ndata: [DONE]\n\n"
		}
		t.Run(name, func(t *testing.T) {
			recorder := &codexTelemetryHTTPRecorder{}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(codexTelemetryHTTPChunkReader{strings.NewReader(body), 7})}
			observeCodexTelemetryHTTPResponse(recorder, response, nil)
			got, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(got))
			observeCodexTelemetryHTTPBody(response, got)
			require.NoError(t, response.Body.Close())
			completeCodexTelemetryHTTPResponse(response, nil)
			require.Len(t, recorder.finished, 1)
			require.Empty(t, recorder.retried)
			result := recorder.finished[0]
			require.Equal(t, "completed", result.Status)
			require.Equal(t, "resp_actual", result.ResponseID)
			require.EqualValues(t, 123, result.InputTokens)
			require.EqualValues(t, 17, result.OutputTokens)
			require.EqualValues(t, 41, result.CachedInputTokens)
			require.EqualValues(t, 9, result.ReasoningOutputTokens)
			require.False(t, result.FirstEventAt.IsZero())
			if stream {
				require.False(t, result.FirstTokenAt.IsZero())
			}
			require.False(t, result.ExplicitClientInterrupt)
		})
	}
}

func TestCodexTelemetryHTTPResponseLargeOutputDoesNotLoseTrailingUsage(t *testing.T) {
	secret := strings.Repeat("x", 2<<20)
	body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_large\",\"status\":\"completed\",\"output\":[{\"content\":[{\"text\":\"" + secret + "\"}]}],\"usage\":{\"input_tokens\":37,\"output_tokens\":800001}}}\n\n"
	recorder := &codexTelemetryHTTPRecorder{}
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	observeCodexTelemetryHTTPResponse(recorder, response, nil)
	written, err := io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.EqualValues(t, len(body), written)
	observeCodexTelemetryHTTPBody(response, []byte(body))
	completeCodexTelemetryHTTPResponse(response, nil)
	require.Len(t, recorder.finished, 1)
	require.EqualValues(t, 800001, recorder.finished[0].OutputTokens)
	require.Empty(t, recorder.retried)
}

func TestCodexTelemetryHTTPResponseNonCompletionOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, body, wantStatus string
		terminal               bool
	}{
		{"failure", `data: {"type":"response.failed","response":{"id":"resp_fail","status":"failed"}}` + "\n\n", "failed", false},
		{"server cancellation", `data: {"type":"response.done","response":{"status":"cancelled"}}` + "\n\n", "cancelled", false},
		{"incomplete", `data: {"type":"response.incomplete","response":{"status":"incomplete"}}` + "\n\n", "incomplete", false},
		{"missing terminal", `data: {"type":"response.created","response":{"id":"resp_partial"}}` + "\n\n", "incomplete", false},
		{"done alone", "data: [DONE]\n\n", "incomplete", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &codexTelemetryHTTPRecorder{}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(test.body))}
			observeCodexTelemetryHTTPResponse(recorder, response, nil)
			_, err := io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			observeCodexTelemetryHTTPBody(response, []byte(test.body))
			require.NoError(t, response.Body.Close())
			completeCodexTelemetryHTTPResponse(response, nil)
			results := recorder.retried
			if test.terminal {
				results = recorder.finished
				require.Empty(t, recorder.retried)
			} else {
				require.Empty(t, recorder.finished)
			}
			require.Len(t, results, 1)
			require.Equal(t, test.wantStatus, results[0].Status)
			require.False(t, results[0].ExplicitClientInterrupt)
		})
	}
}

func TestCodexTelemetryHTTPResponseTransportAndHTTPFailuresRemainRetryable(t *testing.T) {
	for _, code := range []int{0, 429, 500} {
		recorder := &codexTelemetryHTTPRecorder{}
		var response *http.Response
		var sendErr error
		if code == 0 {
			sendErr = errors.New("proxy credential and private URL must not enter summaries")
		} else {
			response = &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private error response"))}
		}
		observeCodexTelemetryHTTPResponse(recorder, response, sendErr)
		require.Empty(t, recorder.finished)
		require.Len(t, recorder.retried, 1)
		require.Equal(t, "failed", recorder.retried[0].Status)
		require.Equal(t, code, recorder.retried[0].HTTPStatus)
		if response != nil {
			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "private error response", string(data))
		}
	}
}

func TestCodexTelemetryHTTPResponseEarlyCloseIsNotUserCancellation(t *testing.T) {
	recorder := &codexTelemetryHTTPRecorder{}
	response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`))}
	observeCodexTelemetryHTTPResponse(recorder, response, nil)
	require.NoError(t, response.Body.Close())
	require.NoError(t, response.Body.Close())
	require.Len(t, recorder.retried, 1)
	require.Equal(t, "incomplete", recorder.retried[0].Status)
	require.False(t, recorder.retried[0].ExplicitClientInterrupt)
}

func TestCodexTelemetryHTTPUsesProgressiveUsageAndLatchesBareErrorBeforeSuccess(t *testing.T) {
	recorder := &codexTelemetryHTTPRecorder{}
	response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}
	observeCodexTelemetryHTTPResponse(recorder, response, nil)
	observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.created","response":{"id":"resp_progress","usage":{"input_tokens":30,"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2}}}}`), "response.created")
	observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"error","error":{"message":"private error"}}`), "error")
	require.Empty(t, recorder.finished)
	require.Empty(t, recorder.retried)
	observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.completed","response":{"status":"completed","service_tier":"flex","usage":{"input_tokens":0,"output_tokens":0}}}`), "response.completed")
	require.NoError(t, response.Body.Close())
	completeCodexTelemetryHTTPResponse(response, nil)
	require.Empty(t, recorder.finished)
	require.Len(t, recorder.retried, 1)
	result := recorder.retried[0]
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "resp_progress", result.ResponseID)
	require.EqualValues(t, 30, result.InputTokens)
	require.EqualValues(t, 5, result.OutputTokens)
	require.EqualValues(t, 2, result.ReasoningOutputTokens)
	require.Equal(t, "flex", result.ServiceTier)
}

func TestCodexTelemetryHTTPCompletedWaitsForGatewayAcceptance(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		recorder := &codexTelemetryHTTPRecorder{}
		response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}
		observeCodexTelemetryHTTPResponse(recorder, response, nil)
		observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.completed","response":{"id":"possibly-empty","status":"completed","output":[]}}`), "response.completed")
		require.NoError(t, response.Body.Close())
		require.Empty(t, recorder.finished, "closing a parsed terminal must not bypass the gateway's failover checks")
		require.Empty(t, recorder.retried)
		var parseErr error
		if rejected {
			parseErr = errors.New("empty completed is retryable")
		}
		completeCodexTelemetryHTTPResponse(response, parseErr)
		completeCodexTelemetryHTTPResponse(response, parseErr)
		if rejected {
			require.Empty(t, recorder.finished)
			require.Len(t, recorder.retried, 1)
			require.Equal(t, "completed", recorder.retried[0].Status)
			require.Equal(t, "rejected", recorder.retried[0].DeliveryStatus)
		} else {
			require.Len(t, recorder.finished, 1)
			require.Empty(t, recorder.retried)
		}
	}
}

func TestCodexTelemetryHTTPContextPreservesInferenceRequest(t *testing.T) {
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	inference := context.Background()
	body := []byte(`{"model":"test","input":"private prompt"}`)
	request, err := http.NewRequestWithContext(inference, http.MethodPost, "https://example.test/responses", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Session_id", "wire-session")
	marked := markCodexTelemetryHTTPRequest(request, caller)
	require.Nil(t, request.Context().Value(codexTelemetryHTTPContextKey{}))
	require.Same(t, caller, marked.Context().Value(codexTelemetryHTTPContextKey{}))
	cancel()
	require.NoError(t, marked.Context().Err())
	got, err := io.ReadAll(marked.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
	require.Equal(t, request.Header, marked.Header)
}

func TestCodexTelemetryHTTPTransportCloseDoesNotPreemptBufferedTerminal(t *testing.T) {
	recorder := &codexTelemetryHTTPRecorder{}
	response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}
	observeCodexTelemetryHTTPResponse(recorder, response, nil)
	beginCodexTelemetryHTTPParsing(response)
	// The cancellation callback can close the transport while Scanner still has
	// an unread terminal in its buffer. Only the parser knows the final outcome.
	require.NoError(t, response.Body.Close())
	require.Empty(t, recorder.retried)
	observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.completed","response":{"id":"buffered-terminal","status":"completed","usage":{"input_tokens":17,"output_tokens":3}}}`), "response.completed")
	completeCodexTelemetryHTTPResponse(response, nil)
	require.Len(t, recorder.finished, 1)
	require.Empty(t, recorder.retried)
	require.Equal(t, "buffered-terminal", recorder.finished[0].ResponseID)
	require.EqualValues(t, 17, recorder.finished[0].InputTokens)
	completeCodexTelemetryHTTPResponse(response, nil)
	require.Len(t, recorder.finished, 1)
}

func TestCodexTelemetryHTTPGatewayUsesActualWireWithFingerprintCollectionOff(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	for _, daily := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, route := range []string{"responses", "messages", "chat"} {
				t.Run(route+"/daily="+strconv.FormatBool(daily)+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
					body := []byte(`{"model":"gpt-5.4","input":"hello","prompt_cache_key":"logical-test","stream":` + strconv.FormatBool(stream) + `}`)
					endpoint := "/v1/responses"
					if route == "messages" || route == "chat" {
						body = []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(stream) + `}`)
						endpoint = "/v1/messages"
						if route == "chat" {
							endpoint = "/v1/chat/completions"
						}
					}
					c, _ := newOpenAIIdentityPathContext(t, endpoint, body, 12)
					caller, cancel := context.WithCancel(c.Request.Context())
					defer cancel()
					c.Request = c.Request.WithContext(caller)
					upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_wire_telemetry", "gpt-5.4")}
					svc, _ := newOpenAIIdentityPathService(t, true, upstream)
					root := "018f5c3c-6e3a-7abf-8def-1234567890ae"
					syncRoot := "018f5c3c-6e3a-7abc-8def-1234567890ab"
					svc.settingService = NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
						SettingKeyEnableOpenAIUUIDv7SessionIdentity:     "true",
						SettingKeyEnableOpenAIOAuthDailySessionRotation: strconv.FormatBool(daily),
					}}, nil)
					svc.oauthDailySessionRepo = &fakeOAuthDailyAffinityRepository{
						pool:     OAuthDailySessionPool{AccountID: 44, BusinessDate: OAuthDailyBusinessDate(time.Now()), Generation: root, SyncSessionID: syncRoot},
						affinity: OAuthDailySessionAffinity{AccountID: 44, APIKeyID: 12, LogicalSessionKey: "logical-test", BusinessDate: OAuthDailyBusinessDate(time.Now()), Generation: root, SlotIndex: 1, StreamSessionID: root},
					}
					telemetry, sent := telemetryCaptureService(t)
					svc.SetCodexTelemetryService(telemetry)
					account := newOpenAIIdentityPathOAuthAccount(44)
					var err error
					switch route {
					case "responses":
						_, err = svc.Forward(caller, c, account, body)
					case "messages":
						_, err = svc.ForwardAsAnthropic(caller, c, account, body, "logical-test", "gpt-5.4")
					case "chat":
						_, err = svc.ForwardAsChatCompletions(caller, c, account, body, "logical-test", "gpt-5.4")
					}
					require.NoError(t, err)
					require.NotNil(t, upstream.lastReq)
					telemetryWaitDrained(t, telemetry)
					calls := sent()
					require.NotEmpty(t, calls)
					wire := codexTelemetryInputFromWire(account, upstream.lastReq.Header, upstream.lastBody, "", false, finalFingerprintCodexWireProfile(upstream.lastReq.Header, upstream.lastBody))
					for _, call := range calls {
						if call.metrics {
							require.Empty(t, call.input.SessionID, "aggregate metrics must not inherit one request's identity")
							require.Empty(t, call.input.ThreadID)
							require.Empty(t, call.input.TurnID)
							continue
						}
						require.Equal(t, wire.SessionID, call.input.SessionID)
						require.Equal(t, wire.ThreadID, call.input.ThreadID)
						require.Equal(t, wire.TurnID, call.input.TurnID)
						require.Equal(t, wire.Model, call.input.Model)
						require.False(t, call.input.WebSocket)
					}
					if daily {
						expected := syncRoot
						// Compatibility conversions select their streaming Responses
						// identity, while Responses retains the original sync request kind.
						if route != "responses" {
							require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
						}
						if stream || route != "responses" {
							expected = root
						}
						require.Equal(t, expected, wire.SessionID)
					}
					require.Empty(t, SnapshotFingerprintObservations(0))
					collector, ok := upstream.resp.Request.Context().Value(codexTelemetryHTTPResponseKey{}).(*codexTelemetryHTTPCollector)
					require.True(t, ok)
					collector.mu.Lock()
					result := collector.stream.result
					collector.mu.Unlock()
					require.Equal(t, "completed", result.Status)
					require.Equal(t, "resp_wire_telemetry", result.ResponseID)
				})
			}
		}
	}
}
