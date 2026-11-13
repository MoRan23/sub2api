package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGrokSafetyUsageDTOHidesUpstreamIDs(t *testing.T) {
	log := &service.UsageLog{GrokSafetyRejection: &service.GrokSafetyRejection{CallID: "internal-call", Code: service.GrokSafetyRejectionCode, Fee: 0.12345678, HTTPStatus: 403, UpstreamRequestID: "upstream-private", UpstreamClientRequestID: "client-private"}}
	user, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	for _, secret := range []string{"internal-call", "upstream-private", "client-private", "call_id", "upstream_request_id", "upstream_client_request_id"} {
		require.NotContains(t, string(user), secret)
	}
	require.Contains(t, string(user), `"fee":0.12345678`)
	require.Contains(t, string(user), `"http_status":403`)
	admin, err := json.Marshal(UsageLogFromServiceAdmin(log))
	require.NoError(t, err)
	require.Contains(t, string(admin), `"upstream_request_id":"upstream-private"`)
	old, err := json.Marshal(UsageLogFromService(&service.UsageLog{}))
	require.NoError(t, err)
	require.NotContains(t, string(old), "grok_safety_rejection")
}
