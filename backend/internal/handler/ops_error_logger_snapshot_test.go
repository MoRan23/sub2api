package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorLoggerLocalBillingSnapshotIsNotUpstreamFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
		Kind: "billing_snapshot_error", Stage: "billing_snapshot", Scope: "local",
		Reason: "billing_snapshot_size_limit", Message: "Engine billing snapshot exceeds storage limit",
	}})
	require.False(t, hasOpsUpstreamErrorContext(c))
	phase, _, owner, _ := classifyOpsErrorLog(c, "internal", "Engine billing snapshot capture failed", "", 500)
	require.NotEqual(t, "upstream", phase)
	require.NotEqual(t, "provider", owner)
}

func TestOpsErrorLoggerRetainsProviderAttributionForMixedOrUnknownEvents(t *testing.T) {
	local := &service.OpsUpstreamErrorEvent{Kind: "billing_snapshot_error", Stage: "billing_snapshot", Scope: "local"}
	provider := &service.OpsUpstreamErrorEvent{Kind: "http_error", Stage: "response", Scope: "provider", UpstreamStatusCode: 502}
	for _, events := range [][]*service.OpsUpstreamErrorEvent{
		{local, provider},
		{{Kind: "other_local_failure", Stage: "local", Scope: "local"}},
		{nil},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set(service.OpsUpstreamErrorsKey, events)
		require.True(t, hasOpsUpstreamErrorContext(c))
	}
}
