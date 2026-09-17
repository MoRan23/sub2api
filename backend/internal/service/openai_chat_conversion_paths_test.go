package service

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Chat compatibility currently sends HTTP Responses requests directly through
// doOpenAIUpstream; it does not enter the native Responses HTTP-to-WS branch.
// Exercise the real physical HTTP boundary instead of manufacturing a WS route.
func TestOpenAIChatConversionPathsDailyRootsMatchObservedWire(t *testing.T) {
	const streamRoot = "018f5c3c-6e3a-7abf-8def-1234567890ae"
	const syncRoot = "018f5c3c-6e3a-7ac0-8def-1234567890af"
	for _, daily := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run("daily="+strconv.FormatBool(daily)+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				enableOpenAIIdentityPathFingerprintObservation(t)
				body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"max_tokens":400,"stream":` + strconv.FormatBool(stream) + `}`)
				original := append([]byte(nil), body...)
				upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_chat_check", "gpt-5.4")}
				svc, _ := newOpenAIIdentityPathService(t, true, upstream)
				svc.settingService = NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
					SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
					SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
					SettingKeyEnableOpenAIOAuthDailySessionRotation:     strconv.FormatBool(daily),
				}}, nil)
				businessDate := OAuthDailyBusinessDate(time.Now())
				svc.oauthDailySessionRepo = &fakeOAuthDailyAffinityRepository{
					pool: OAuthDailySessionPool{AccountID: 9801, BusinessDate: businessDate, Generation: streamRoot, SyncSessionID: syncRoot},
					affinity: OAuthDailySessionAffinity{AccountID: 9801, APIKeyID: 98, LogicalSessionKey: "chat-check-path",
						BusinessDate: businessDate, Generation: streamRoot, SlotIndex: 1, StreamSessionID: streamRoot},
				}
				c, recorder := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 98)
				account := newOpenAIIdentityPathOAuthAccount(9801)
				account.Extra = map[string]any{openAIPinnedInstallationIDKey: transportTestPinnedInstallationID}

				result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "chat-check-path", "gpt-5.4")

				require.NoError(t, err, recorder.Body.String())
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1, "one real HTTP physical attempt")
				require.Equal(t, original, body, "conversion diagnostics must not mutate the caller's buffer")
				require.Equal(t, http.MethodPost, upstream.lastReq.Method)
				require.Contains(t, upstream.lastReq.URL.Path, "/responses")
				require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(), "the existing Chat adapter always streams its upstream Responses request")
				identity := requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
				plan, exists := OpenAIOAuthIdentityPlanFromContext(c)
				require.True(t, exists)
				require.Equal(t, plan.TurnIdentity.SessionID, identity.SessionID)
				require.Equal(t, plan.TurnIdentity.ThreadID, identity.ThreadID)
				entry := requireOpenAIIdentityPathSingleFingerprintObservation(t, identity, "POST /v1/chat/completions")
				require.Equal(t, FingerprintObservationEventHTTP, entry.EventKind)
				require.NotNil(t, entry.ConversionCheck)
				require.Equal(t, "known_loss", entry.ConversionCheck.Status)
				require.Equal(t, GetOpenAIChatConversionCheck(c), entry.ConversionCheck)
				require.False(t, gjson.GetBytes(upstream.lastBody, "conversion_check").Exists(), "diagnostics are local observations, never upstream request metadata")
				require.False(t, gjson.GetBytes(upstream.lastBody, "client_metadata.conversion_check").Exists())
				require.Equal(t, daily, entry.DailyFixedRootEnabled)
				if daily {
					// This existing compatibility adapter uses its streaming root even
					// when the downstream caller requested a buffered Chat response.
					require.Equal(t, streamRoot, identity.SessionID)
					require.Equal(t, "stream", entry.DailyFixedRootKind)
					require.Equal(t, businessDate, entry.DailyFixedRootBusinessDate)
					require.Equal(t, streamRoot, entry.DailyFixedRootSessionID)
				} else {
					require.NotEqual(t, streamRoot, identity.SessionID)
					require.NotEqual(t, syncRoot, identity.SessionID)
					require.Empty(t, entry.DailyFixedRootSessionID)
				}
			})
		}
	}
}

func TestOpenAIChatConversionPathsAPIKeyRawChatPreservesUnsupportedOAuthFields(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","name":"named-user","content":"hello"}],"n":2,"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[]}}}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_raw_check","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)),
	}}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	account := newOpenAIIdentityPathAPIKeyAccount(9802)
	account.Extra["openai_responses_supported"] = false
	c, recorder := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 98)
	PrepareOpenAIChatConversionCheck(c, body)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err, recorder.Body.String())
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
	require.JSONEq(t, string(body), string(upstream.lastBody), "the OAuth-only rejection contract must not strip or remap a raw API key request")
	require.Nil(t, GetOpenAIChatConversionCheck(c))
	for _, entry := range SnapshotFingerprintObservations(0) {
		require.Nil(t, entry.ConversionCheck)
	}
}

func TestOpenAIChatConversionPathsResponsesShapePreservesInput(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"n":2,"input":[{"role":"user","content":"keep native input"}]}`)
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_chat_native_check", "gpt-5.4")}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	c, recorder := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 98)
	PrepareOpenAIChatConversionCheck(c, body)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, newOpenAIIdentityPathOAuthAccount(9803), body, "native-chat-path", "gpt-5.4")

	require.NoError(t, err, recorder.Body.String())
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "keep native input", gjson.GetBytes(upstream.lastBody, "input.0.content").String())
	require.Nil(t, GetOpenAIChatConversionCheck(c))
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].ConversionCheck, "a native Responses-shaped request was not converted from Chat")
}
