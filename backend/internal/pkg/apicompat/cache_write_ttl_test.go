package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesCacheWriteTTLDecodeAndChatRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, details string
		short, long   int
	}{
		{"mixed", `{"ttl_30m_tokens":4,"ttl_12h_tokens":6,"vendor":"kept"}`, 4, 6},
		{"invalid", `{"ttl_30m_tokens":4,"ttl_12h_tokens":7}`, 0, 0},
		{"missing_bucket", `{"ttl_12h_tokens":10}`, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var usage ResponsesUsage
			err := json.Unmarshal([]byte(`{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":`+tt.details+`}}`), &usage)
			require.NoError(t, err)
			require.Equal(t, 10, usage.CacheCreationInputTokens)
			require.Equal(t, tt.short, usage.CacheCreation30mInputTokens)
			require.Equal(t, tt.long, usage.CacheCreation12hInputTokens)
			chat := &ChatUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, PromptTokensDetails: promptDetailsFromResponses(usage.InputTokensDetails)}
			back := ChatUsageToResponsesUsage(chat)
			require.Equal(t, usage.CacheCreationInputTokens, back.CacheCreationInputTokens)
			require.Equal(t, tt.short, back.CacheCreation30mInputTokens)
			require.Equal(t, tt.long, back.CacheCreation12hInputTokens)
			require.JSONEq(t, tt.details, string(back.InputTokensDetails.CacheWriteTokensDetails))
			// Conversion results cannot retain a mutable view of the source bytes.
			usage.InputTokensDetails.CacheWriteTokensDetails[0] = '!'
			chat.PromptTokensDetails.CacheWriteTokensDetails[0] = '!'
			require.JSONEq(t, tt.details, string(back.InputTokensDetails.CacheWriteTokensDetails))
		})
	}
}

func TestResponsesCacheWriteTTLAliasAndCanonicalAggregate(t *testing.T) {
	t.Parallel()
	var usage ResponsesUsage
	require.NoError(t, json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`), &usage))
	require.Equal(t, 4, usage.CacheCreation30mInputTokens)
	require.Equal(t, 6, usage.CacheCreation12hInputTokens)
	require.NoError(t, json.Unmarshal([]byte(`{"cache_creation_input_tokens":10,"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":0,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`), &usage))
	require.Zero(t, usage.CacheCreationInputTokens, "canonical zero overrides legacy aggregate")
	require.Zero(t, usage.CacheCreation30mInputTokens)
	require.Zero(t, usage.CacheCreation12hInputTokens)
}
