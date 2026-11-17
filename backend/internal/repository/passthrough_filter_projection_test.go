//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPassthroughFilterProjectionAndBackgroundProtection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		extra := map[string]any{"openai_passthrough_model_filter": enabled}
		require.Empty(t, accountConfigurationExtraPatch(context.Background(), []int64{1}, extra))
		require.Equal(t, extra, filterSchedulerExtra(extra))
		require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(extra))
		require.Len(t, extra, 1, "the background guard must not mutate its input")
	}
	require.Contains(t, catalogExtraFields, "openai_passthrough_model_filter")
	require.Contains(t, catalogAccountProjectionSQL, "'openai_passthrough_model_filter'")
	capabilities := map[string]any{"openai_capabilities": map[string]any{"seedance": true}}
	require.Equal(t, capabilities, filterSchedulerCredentials(capabilities))
}
