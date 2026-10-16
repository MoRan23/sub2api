package service

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConfiguredMinOutputTokensPreservesTimezoneAdapters(t *testing.T) {
	previous := IsFingerprintObservationEnabled()
	t.Cleanup(func() { SetFingerprintObservationEnabled(previous) })
	cfg := &config.Config{}
	cfg.Gateway.MinOutputTokens = 64
	for _, observe := range []bool{false, true} {
		SetFingerprintObservationEnabled(observe)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		SetRequestTimezoneState(c, &RequestTimezoneState{})
		maxTokens := 16
		chat := &apicompat.ChatCompletionsRequest{
			Model: "gpt-5.4", MaxTokens: &maxTokens,
			Messages: []apicompat.ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		}
		out, err := chatCompletionsToResponsesWithTimezoneObservation(cfg, c, chat)
		require.NoError(t, err)
		require.NotNil(t, out.MaxOutputTokens)
		require.Equal(t, 64, *out.MaxOutputTokens, "observation=%v", observe)

		anthropic := &apicompat.AnthropicRequest{
			Model: "gpt-5.4", MaxTokens: 16,
			Messages: []apicompat.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		}
		out, err = anthropicToResponsesWithTimezoneObservation(cfg, c, anthropic)
		require.NoError(t, err)
		require.Equal(t, 64, *out.MaxOutputTokens, "observation=%v", observe)
		chatOut, err := anthropicToChatCompletionsWithTimezoneObservation(cfg, c, anthropic)
		require.NoError(t, err)
		require.Equal(t, 64, *chatOut.MaxCompletionTokens, "observation=%v", observe)
		require.Equal(t, 16, anthropic.MaxTokens)
		require.Equal(t, 16, *chat.MaxTokens)
	}
}

func TestConfiguredWebSocketCompatibilityPreservesFinalModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.MinOutputTokens = 64
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-sol": "gpt-5.5"}}}
	body := []byte(`{"model":"gpt-6-sol","input":"hello","reasoning":{"mode":"pro"}}`)
	original := string(body)
	got, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBodyWithConfig(cfg, body, account, false, "gpt-6-sol")
	require.NoError(t, err)
	require.Equal(t, "pro", gjson.GetBytes(got, "reasoning.mode").String())
	require.Equal(t, original, string(body))
	legacy, err := convertLegacyResponsesMessages(cfg, []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`))
	require.NoError(t, err)
	require.Equal(t, 64, *legacy.maxOutputTokens)
}
