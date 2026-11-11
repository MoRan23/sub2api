package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestScopedAdminUsageResponseIncludesCacheWriteTTLWithoutRelations(t *testing.T) {
	usage := &service.UsageLog{
		CacheCreationTokens:    10,
		CacheCreation30mTokens: 4,
		CacheCreation12hTokens: 6,
		User:                   &service.User{Email: "private@example.test"},
		APIKey:                 &service.APIKey{Key: "private-test-key"},
	}
	got := scopedAdminUsageLogResponse(usage)
	require.Equal(t, 10, got["cache_creation_tokens"])
	require.Equal(t, 4, got["cache_creation_30m_tokens"])
	require.Equal(t, 6, got["cache_creation_12h_tokens"])
	require.NotContains(t, got, "user")
	require.NotContains(t, got, "api_key")
	require.NotNil(t, usage.User, "response projection must not mutate the source")
	require.NotNil(t, usage.APIKey)
}
