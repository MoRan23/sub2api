package openai_ws_v2

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelayCacheWriteTTLPerTurnAndAggregate(t *testing.T) {
	t.Parallel()
	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}}}`), "response.completed", nil)
	first := finalizeRelayTurnUsage(state)
	require.Equal(t, 10, first.CacheCreationInputTokens)
	require.Equal(t, 4, first.CacheCreation30mInputTokens)
	require.Equal(t, 6, first.CacheCreation12hInputTokens)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":120,"output_tokens":3,"input_tokens_details":{"cache_write_tokens":8,"cache_write_tokens_details":{"ttl_30m_tokens":0,"ttl_12h_tokens":8}}}}}`), "response.completed", nil)
	second := finalizeRelayTurnUsage(state)
	require.Equal(t, 0, second.CacheCreation30mInputTokens)
	require.Equal(t, 8, second.CacheCreation12hInputTokens)
	require.Equal(t, 18, state.usage.CacheCreationInputTokens)
	require.Equal(t, 4, state.usage.CacheCreation30mInputTokens)
	require.Equal(t, 14, state.usage.CacheCreation12hInputTokens)
	require.Equal(t, Usage{}, finalizeRelayTurnUsage(state), "turn finalization must not bill twice")
	require.Equal(t, 18, state.usage.CacheCreationInputTokens)
}

func TestRelayCacheWriteTTLFinalAggregateClearsEarlierBreakdown(t *testing.T) {
	t.Parallel()
	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}}}`), "response.in_progress", nil)
	require.Equal(t, 6, state.turnUsage.CacheCreation12hInputTokens)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":12}}}}`), "response.completed", nil)
	final := finalizeRelayTurnUsage(state)
	require.Equal(t, 12, final.CacheCreationInputTokens)
	require.Zero(t, final.CacheCreation30mInputTokens)
	require.Zero(t, final.CacheCreation12hInputTokens)
}
