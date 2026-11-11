package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheCreationTTLTokens(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body             string
		aggregate, short, long int
	}{
		{"mixed", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`, 10, 4, 6},
		{"chat_alias", `{"prompt_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":0,"ttl_12h_tokens":10}}}`, 10, 0, 10},
		{"missing", `{}`, 10, 0, 0},
		{"partial", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_12h_tokens":10}}}`, 10, 0, 0},
		{"wrong_sum", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":5}}}`, 10, 0, 0},
		{"negative", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":-1,"ttl_12h_tokens":11}}}`, 10, 0, 0},
		{"fractional", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":4.5,"ttl_12h_tokens":5.5}}}`, 10, 0, 0},
		{"numeric_string", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":"4","ttl_12h_tokens":6}}}`, 10, 0, 0},
		{"overflow", `{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":1e30,"ttl_12h_tokens":0}}}`, 10, 0, 0},
		{"canonical_invalid_not_replaced_by_alias", `{"input_tokens_details":{"cache_write_tokens_details":null},"prompt_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":4,"ttl_12h_tokens":6}}}`, 10, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, b := CacheCreationTTLTokens(gjson.Parse(tt.body), tt.aggregate)
			require.Equal(t, tt.short, a)
			require.Equal(t, tt.long, b)
		})
	}
}
