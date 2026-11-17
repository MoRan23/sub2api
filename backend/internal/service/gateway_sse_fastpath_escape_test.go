//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicSSEFastPathKeepsEscapedFieldSemantics(t *testing.T) {
	for _, payload := range []string{
		`{"type":"content_block_stop","us\u0061ge":{"output_tokens":7}}`,
		`{"type":"content_block_stop","m\u006fdel":"upstream-model"}`,
	} {
		require.True(t, sseEventNeedsJSONParse("content_block_stop", []byte(payload), false, false))
	}
	require.False(t, sseEventNeedsJSONParse("content_block_stop", []byte(`{"type":"content_block_stop","index":0}`), false, false))
}
