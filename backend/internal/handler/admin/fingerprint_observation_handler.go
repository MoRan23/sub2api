package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	fingerprintObservationDefaultPageSize = 20
	fingerprintObservationMaxPageSize     = 100
)

type fingerprintObservationsResponse struct {
	Enabled     bool                                        `json:"enabled"`
	Items       []service.FingerprintObservationSessionNode `json:"items"`
	Total       int                                         `json:"total"`
	Page        int                                         `json:"page"`
	PageSize    int                                         `json:"page_size"`
	Pages       int                                         `json:"pages"`
	SnapshotSeq uint64                                      `json:"snapshot_seq"`
}

// ListFingerprintObservations returns a sequence-bounded page of OpenAI OAuth
// root sessions and their observed thread tree. The ring is process-local and
// is cleared whenever fingerprint observation is disabled.
// GET /api/v1/admin/openai/fingerprint-observations
func (h *OpenAIOAuthHandler) ListFingerprintObservations(c *gin.Context) {
	page := 1
	if raw := c.Query("page"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			page = parsed
		}
	}
	pageSize := fingerprintObservationDefaultPageSize
	if raw := c.Query("page_size"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			pageSize = parsed
			if pageSize > fingerprintObservationMaxPageSize {
				pageSize = fingerprintObservationMaxPageSize
			}
		}
	}
	var snapshotSeq uint64
	if raw := c.Query("snapshot_seq"); raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
			snapshotSeq = parsed
		}
	}
	snapshot := service.SnapshotFingerprintObservationSessions(page, pageSize, snapshotSeq)
	response.Success(c, fingerprintObservationsResponse{
		Enabled:     service.IsFingerprintObservationEnabled(),
		Items:       snapshot.Items,
		Total:       snapshot.Total,
		Page:        snapshot.Page,
		PageSize:    snapshot.PageSize,
		Pages:       snapshot.Pages,
		SnapshotSeq: snapshot.SnapshotSeq,
	})
}
