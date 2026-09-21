package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeOpenAIJSONUseNumberRejectsTrailingDocument(t *testing.T) {
	var decoded map[string]any
	require.Error(t, decodeOpenAIJSONUseNumber([]byte(`{"name":"python"}{"extra":true}`), &decoded))
}
