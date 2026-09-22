package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func chatConversionTestContext(body []byte) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	return c
}

func TestOpenAIChatConversionCheckIsFrozenAndAccountScoped(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"private prompt"}],"max_tokens":400,"temperature":0.2}`)
	c := chatConversionTestContext(body)
	PrepareOpenAIChatConversionCheck(c, body)
	PrepareOpenAIChatConversionCheck(c, []byte(`{"messages":[{"role":"invalid","content":"changed"}]}`))
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.NoError(t, ValidateOpenAIChatConversionForAccount(c, account))
	got := GetOpenAIChatConversionCheck(c)
	require.NotNil(t, got)
	require.Equal(t, "known_loss", got.Status)
	require.NotEmpty(t, got.Issues)
	original := got.Issues[0]
	got.Issues[0].Reason = "mutated"
	require.Equal(t, original, GetOpenAIChatConversionCheck(c).Issues[0])
	raw, err := json.Marshal(GetOpenAIChatConversionCheck(c))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private prompt")
	account.Type = AccountTypeAPIKey
	require.NoError(t, ValidateOpenAIChatConversionForAccount(c, account))
	require.Nil(t, GetOpenAIChatConversionCheck(c), "a later API key attempt must not inherit OAuth diagnostics")
}

func TestOpenAIChatConversionCheckSkipsResponsesShape(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","n":2}`)
	c := chatConversionTestContext(body)
	PrepareOpenAIChatConversionCheck(c, body)
	require.NoError(t, ValidateOpenAIChatConversionForAccount(c, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.Nil(t, GetOpenAIChatConversionCheck(c))
}

func TestOpenAIChatConversionRejectedBeforeAnyUpstreamAttempt(t *testing.T) {
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	for _, observe := range []bool{false, true} {
		SetFingerprintObservationEnabled(false)
		SetFingerprintObservationEnabled(observe)
		body := []byte(`{"model":"gpt-5.4","messages":[{"role":"unknown","content":"private prompt"}]}`)
		c := chatConversionTestContext(body)
		upstream := &httpUpstreamRecorder{}
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		account := &Account{ID: 23, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
		var conversionErr *apicompat.ChatConversionError
		require.ErrorAs(t, err, &conversionErr)
		require.NotContains(t, err.Error(), "private prompt")
		require.Nil(t, result)
		require.Nil(t, upstream.lastReq)
		require.Empty(t, SnapshotFingerprintObservations(0))
		require.Nil(t, GetOpenAIChatConversionCheck(c))
	}
}

func TestOpenAIChatConversionKnownLossAttachedToPhysicalObservations(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"max_tokens":400,"stop":"private stop","stream":false}`)
	forwardOAuthChatCompletionsForUpstreamBody(t, body)
	entries := SnapshotFingerprintObservations(0)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		require.NotNil(t, entry.ConversionCheck)
		require.Equal(t, "known_loss", entry.ConversionCheck.Status)
		raw, err := json.Marshal(entry.ConversionCheck)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private stop")
	}
	entries[0].ConversionCheck.Issues[0].Reason = "mutated snapshot"
	require.NotEqual(t, "mutated snapshot", SnapshotFingerprintObservations(0)[0].ConversionCheck.Issues[0].Reason)
}

func TestOpenAIChatOAuthKeepsLaterSystemAtOriginalPosition(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"initial policy"},{"role":"user","content":"first"},{"role":"system","content":"later policy"},{"role":"assistant","content":"answer"},{"role":"developer","content":"developer policy"},{"role":"user","content":"next"}]}`)
	out := forwardOAuthChatCompletionsForUpstreamBody(t, body)
	require.Equal(t, "initial policy", gjson.GetBytes(out, "instructions").String())
	require.Equal(t, "user", gjson.GetBytes(out, "input.0.role").String())
	require.Equal(t, "developer", gjson.GetBytes(out, "input.1.role").String())
	require.Equal(t, "later policy", gjson.GetBytes(out, "input.1.content").String())
	require.Equal(t, "assistant", gjson.GetBytes(out, "input.2.role").String())
	require.Equal(t, "developer", gjson.GetBytes(out, "input.3.role").String())
	require.Equal(t, "developer policy", gjson.GetBytes(out, "input.3.content").String())
}

func TestOpenAIChatOAuthAllowedToolsPreservesDefinitionsAndNames(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"use python"}],"tools":[{"type":"function","function":{"name":"python","parameters":{"type":"object"}}},{"type":"function","function":{"name":"other","strict":true,"parameters":{"type":"object"}}}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"python"}}]}}}`)
	out := forwardOAuthChatCompletionsForUpstreamBody(t, body)
	require.Equal(t, int64(2), gjson.GetBytes(out, "tools.#").Int())
	require.Equal(t, "other", gjson.GetBytes(out, "tools.1.name").String())
	require.True(t, gjson.GetBytes(out, "tools.1.strict").Bool())
	require.Equal(t, "allowed_tools", gjson.GetBytes(out, "tool_choice.type").String())
	require.Equal(t, "required", gjson.GetBytes(out, "tool_choice.mode").String())
	require.False(t, gjson.GetBytes(out, "tool_choice.allowed_tools").Exists())
	require.Equal(t, "python", gjson.GetBytes(out, "tools.0.name").String())
	require.Equal(t, "python", gjson.GetBytes(out, "tool_choice.tools.0.name").String())
}

func TestLegacyResponsesIngressMapsNestedAllowedTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"lookup"}}]}}}`)
	out, changed, err := normalizeOpenAIResponsesLegacyIngress(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice.mode").String())
	require.Equal(t, "lookup", gjson.GetBytes(out, "tool_choice.tools.0.name").String())
	require.False(t, gjson.GetBytes(out, "tool_choice.allowed_tools").Exists())
}
