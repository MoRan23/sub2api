package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// installationObservationDefaultLimit bounds the default snapshot returned to the
// panel so a full 500-entry buffer isn't shipped on every poll.
const installationObservationDefaultLimit = 200

// installationObservationsResponse is the read-only view served to the panel.
// enabled lets the frontend render the correct empty state ("observation off"
// vs "on but no traffic yet") without a second round-trip.
type installationObservationsResponse struct {
	Enabled bool                                   `json:"enabled"`
	Entries []service.InstallationObservationEntry `json:"entries"`
}

// ListInstallationObservations returns the most recent buffered OpenAI OAuth
// installation_id observations (newest first). The buffer only holds data while
// observation is enabled, so a disabled observer returns enabled=false and an
// empty list.
// GET /api/v1/admin/openai/installation-observations
func (h *OpenAIOAuthHandler) ListInstallationObservations(c *gin.Context) {
	limit := installationObservationDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	response.Success(c, installationObservationsResponse{
		Enabled: service.IsInstallationObservationEnabled(),
		Entries: service.SnapshotInstallationObservations(limit),
	})
}
