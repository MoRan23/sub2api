package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAIRequestTimezoneIngressSnapshotSurvivesBootstrap(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{
		map[string]any{"role": "user", "content": timezoneTestEnvironment("Asia/Shanghai", "2020-01-01")},
		map[string]any{"type": "function_call_output", "output": "heartbeat"},
	}})
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 10)
	svc := &OpenAIGatewayService{}
	svc.CaptureOpenAIRequestTimezone(c, body)
	adapted, err := sjson.SetBytes(body, "input.1", map[string]any{"role": "user", "content": "heartbeat"})
	require.NoError(t, err)
	out := svc.prepareOpenAIRequestTimezone(c.Request.Context(), c, newOpenAIIdentityPathAPIKeyAccount(1), adapted, false)
	require.JSONEq(t, string(adapted), string(out), "bootstrap role changes must not promote a historical environment")
}

func TestOpenAIRequestTimezoneIngressSnapshotMapsCompactionReorder(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{
		map[string]any{"type": "compaction_trigger"},
		map[string]any{"role": "user", "content": timezoneTestEnvironment("Asia/Shanghai", "2020-01-01")},
	}, "model": "original"})
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 11)
	svc := &OpenAIGatewayService{}
	svc.CaptureOpenAIRequestTimezone(c, body)
	value, _ := c.Get(openAIRequestTimezoneCaptureKey)
	value.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
	adapted, changed, err := NormalizeCompactionTriggerInputOrder(body)
	require.NoError(t, err)
	require.True(t, changed)
	MarkOpenAIRequestTimezoneCompactionReorder(c)
	adapted, err = sjson.SetBytes(adapted, "model", "mapped-account-model")
	require.NoError(t, err)
	out := svc.prepareOpenAIRequestTimezone(c.Request.Context(), c, newOpenAIIdentityPathAPIKeyAccount(1), adapted, false)
	require.Contains(t, gjson.GetBytes(out, "input.0.content").String(), "<current_date>2026-09-09</current_date>")
	require.Contains(t, gjson.GetBytes(out, "input.0.content").String(), OpenAIRequestTimezone)
	require.Equal(t, "compaction_trigger", gjson.GetBytes(out, "input.1.type").String())
	require.Equal(t, "mapped-account-model", gjson.GetBytes(out, "model").String())
	second := svc.prepareOpenAIRequestTimezone(context.Background(), c, newOpenAIIdentityPathAPIKeyAccount(2), adapted, false)
	require.Equal(t, string(out), string(second), "account failover must reuse the same date and source mapping")
}

func TestOpenAIRequestTimezonePolicyIsFrozenAndPassthroughIndependent(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestEnvironment("Asia/Shanghai", "2020-01-01")})
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 12)
	policy := openai.DefaultRequestPolicy()
	policy.PassthroughTimezoneConversionEnabled = false
	c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), policy))
	svc := &OpenAIGatewayService{}
	svc.CaptureOpenAIRequestTimezone(c, body)
	value, _ := c.Get(openAIRequestTimezoneCaptureKey)
	value.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
	account := newOpenAIIdentityPathAPIKeyAccount(1)
	require.Equal(t, body, svc.prepareOpenAIRequestTimezone(context.Background(), c, account, body, true))
	out := svc.prepareOpenAIRequestTimezone(context.Background(), c, account, body, false)
	require.Contains(t, gjson.GetBytes(out, "input").String(), "2026-09-09")
	state, ok := RequestTimezoneStateFromContext(c)
	require.True(t, ok)
	require.Equal(t, timezoneTestAcceptedAt(), state.AcceptedAt)
	require.Equal(t, policy, state.Policy)
	other := newOpenAIIdentityPathAPIKeyAccount(2)
	other.Platform = PlatformGrok
	require.Equal(t, body, svc.prepareOpenAIRequestTimezone(context.Background(), c, other, body, false))
}

type timezoneWireUpstream struct {
	HTTPUpstream
	client *http.Client
	target *url.URL
}

func (s *timezoneWireUpstream) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.URL.Scheme, request.URL.Host = s.target.Scheme, s.target.Host
	request.Host = s.target.Host
	return s.client.Do(request)
}

func TestOpenAIRequestTimezoneHTTPWire(t *testing.T) {
	for _, route := range []string{"responses", "oauth", "compact", "passthrough", "chat", "messages", "raw_chat"} {
		t.Run(route, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			payload := map[string]any{"model": "gpt-5.4", "stream": false, "prompt_cache_key": "timezone-wire-" + route}
			input := []any{map[string]any{"role": "user", "content": timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")}, map[string]any{"role": "user", "content": "What is today's date?"}}
			path := "/custom/responses"
			if route == "chat" || route == "messages" || route == "raw_chat" {
				payload["messages"] = input
				path = "/v1/chat/completions"
			} else {
				payload["input"] = input
			}
			if route == "messages" {
				path = "/v1/messages"
				payload["max_tokens"] = 64
			}
			if route == "compact" {
				path = "/v1/responses/compact"
			}
			body := timezoneTestBody(t, payload)
			c, _ := newOpenAIIdentityPathContext(t, path, body, 30)
			c.Request.Header.Set(openai.CodexResidencyHeaderName, "eu")
			type receipt struct {
				body    []byte
				headers http.Header
			}
			received := make(chan receipt, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				received <- receipt{raw, r.Header.Clone()}
				if route == "raw_chat" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"chatcmpl-test","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
					return
				}
				response := successfulInstallationTestResponse()
				if route == "chat" || route == "messages" {
					response = openAICompatSSECompletedResponse("resp_timezone", "gpt-5.4")
				}
				defer response.Body.Close()
				for k, values := range response.Header {
					w.Header()[k] = values
				}
				w.WriteHeader(response.StatusCode)
				_, _ = io.Copy(w, response.Body)
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			svc, _ := newOpenAIIdentityPathService(t, true, nil)
			svc.httpUpstream = &timezoneWireUpstream{client: server.Client(), target: target}
			account := newOpenAIIdentityPathAPIKeyAccount(71)
			if route == "oauth" || route == "chat" || route == "messages" || route == "compact" {
				account = newOpenAIIdentityPathOAuthAccount(72)
			}
			if route == "passthrough" {
				account.Extra = map[string]any{"openai_passthrough": true}
			}
			if route == "raw_chat" {
				account.Extra = map[string]any{"openai_responses_supported": false}
			}
			svc.CaptureOpenAIRequestTimezone(c, body)
			capture, _ := c.Get(openAIRequestTimezoneCaptureKey)
			capture.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
			switch route {
			case "chat", "raw_chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "timezone-wire", "")
			case "messages":
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "timezone-wire", "")
			default:
				_, err = svc.Forward(context.Background(), c, account, body)
			}
			require.NoError(t, err)
			select {
			case actual := <-received:
				require.Equal(t, []string{"us"}, actual.headers.Values(openai.CodexResidencyHeaderName))
				scan := ScanOpenAIRequestTimezones(actual.body)
				require.Equal(t, "complete", scan.ScanStatus)
				require.Len(t, scan.Items, 1)
				require.Equal(t, OpenAIRequestTimezone, scan.Items[0].Value)
				require.Equal(t, "2026-09-09", scan.Items[0].CurrentDate)
				entries := SnapshotFingerprintObservations(0)
				require.NotEmpty(t, entries)
				require.Equal(t, "us", entries[0].OutboundCodexResidency)
				require.NotNil(t, entries[0].InboundTimezoneObservations)
				require.Equal(t, "Asia/Shanghai", entries[0].InboundTimezoneObservations.Items[0].Value)
				require.Equal(t, OpenAIRequestTimezone, entries[0].OutboundTimezoneObservations.Items[0].Value)
				require.Equal(t, "matched", entries[0].TimezoneComparisonStatus)
			case <-time.After(time.Second):
				t.Fatal("no upstream request received")
			}
		})
	}
}

func TestOpenAIRequestTimezoneChatObservationTracksRemovedTool(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_preview","user_location":{"timezone":"Asia/Shanghai"}},{"type":"web_search","user_location":{"timezone":"Europe/London"}}]}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 31)
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_timezone_tools", "gpt-5.4")}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, newOpenAIIdentityPathOAuthAccount(73), body, "timezone-tools", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "tools.#").Int())
	require.Equal(t, OpenAIRequestTimezone, gjson.GetBytes(upstream.lastBody, "tools.0.user_location.timezone").String())
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 1)
	require.Len(t, entries[0].TimezoneConversions, 2)
	require.Equal(t, "not_sent", entries[0].TimezoneConversions[0].Status)
	require.Equal(t, "adapter_removed_source", entries[0].TimezoneConversions[0].Reason)
	require.Equal(t, "converted", entries[0].TimezoneConversions[1].Status)
}
