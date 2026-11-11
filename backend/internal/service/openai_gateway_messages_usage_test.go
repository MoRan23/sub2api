//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestCopyOpenAIUsageFromResponsesUsageTrustsCanonicalCacheCreationValue(t *testing.T) {
	usage := &apicompat.ResponsesUsage{
		InputTokens:              20,
		OutputTokens:             2,
		CacheCreationInputTokens: 0,
		InputTokensDetails: &apicompat.ResponsesInputTokensDetails{
			CachedTokens:     3,
			CacheWriteTokens: 19,
		},
	}

	got := copyOpenAIUsageFromResponsesUsage(usage)

	require.Equal(t, 20, got.InputTokens)
	require.Equal(t, 3, got.CacheReadInputTokens)
	require.Zero(t, got.CacheCreationInputTokens)
}

func TestCopyOpenAIUsageFromResponsesUsageKeepsCacheWriteTTL(t *testing.T) {
	var usage apicompat.ResponsesUsage
	require.NoError(t, json.Unmarshal([]byte(`{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`), &usage))
	got := copyOpenAIUsageFromResponsesUsage(&usage)
	require.Equal(t, 10, got.CacheCreationInputTokens)
	require.Equal(t, 4, got.CacheCreation30mInputTokens)
	require.Equal(t, 6, got.CacheCreation12hInputTokens)
}
