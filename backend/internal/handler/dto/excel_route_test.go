package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountExtraHidesPrivateRouteGeneration(t *testing.T) {
	extra := map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: true, service.OpenAIUpstreamRouteGenerationExtraKey: "private"}
	public := redactAccountManagedExtra(extra)
	require.Equal(t, true, public[service.OpenAIExcelUpstreamEnabledExtraKey])
	require.NotContains(t, public, service.OpenAIUpstreamRouteGenerationExtraKey)
	require.Contains(t, extra, service.OpenAIUpstreamRouteGenerationExtraKey)
}
