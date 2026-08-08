package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// fingerprintObservationDefaultLimit keeps polling payloads bounded while
// allowing operators to request a smaller positive limit explicitly.
const fingerprintObservationDefaultLimit = 200

type fingerprintObservationsResponse struct {
	Enabled bool                                  `json:"enabled"`
	Entries []service.FingerprintObservationEntry `json:"entries"`
}

// ListFingerprintObservations returns the newest buffered OpenAI OAuth
// fingerprints. The ring is process-local and is cleared whenever the global
// fingerprint-observation compatibility setting is disabled.
// GET /api/v1/admin/openai/fingerprint-observations
func (h *OpenAIOAuthHandler) ListFingerprintObservations(c *gin.Context) {
	limit := fingerprintObservationDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	response.Success(c, fingerprintObservationsResponse{
		Enabled: service.IsFingerprintObservationEnabled(),
		Entries: service.SnapshotFingerprintObservations(limit),
	})
}
