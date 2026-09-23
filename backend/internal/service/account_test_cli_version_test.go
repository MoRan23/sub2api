package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

func TestAccountTestPayloadUsesFrozenClientVersion(t *testing.T) {
	// A runtime version update between header capture and payload construction
	// must not change metadata.user_id's format within the same request.
	withCLIVersionResolverForTest(t, func() string { return "9.9.9" })
	for _, tc := range []struct {
		ua     string
		isJSON bool
	}{
		{"claude-cli/2.1.77 (external, cli)", false},
		{"claude-cli/2.1.78 (external, cli)", true},
		{claude.DefaultHeaders()["User-Agent"], true},
	} {
		payload, err := createTestPayloadForUserAgent("claude-sonnet-4-6", tc.ua)
		require.NoError(t, err)
		metadata, ok := payload["metadata"].(map[string]string)
		require.True(t, ok)
		parsed := ParseMetadataUserID(metadata["user_id"])
		require.NotNil(t, parsed)
		require.Equal(t, tc.isJSON, parsed.IsNewFormat)
	}
}
