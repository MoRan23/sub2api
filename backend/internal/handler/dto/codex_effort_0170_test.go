package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminOpaqueReasoningEffortDifference(t *testing.T) {
	for _, pair := range [][2]string{{"custom-a", "custom-b"}, {"32768", "16384"}, {"disabled", "none"}, {"Custom", "custom"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			requested, effective := pair[0], pair[1]
			got := adminUpstreamReasoningEffort(&service.UsageLog{RequestedReasoningEffort: &requested, ReasoningEffort: &effective})
			require.NotNil(t, got)
			require.Equal(t, effective, *got)
		})
	}
	for _, pair := range [][2]string{{"custom-a", "custom-a"}, {"high", "HIGH"}} {
		requested, effective := pair[0], pair[1]
		require.Nil(t, adminUpstreamReasoningEffort(&service.UsageLog{RequestedReasoningEffort: &requested, ReasoningEffort: &effective}))
	}
}
