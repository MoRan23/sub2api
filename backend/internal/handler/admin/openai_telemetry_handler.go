package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ListTelemetryObservations exposes sanitized process-local send summaries.
// It never changes collection state or dispatches upstream telemetry.
func (h *OpenAIOAuthHandler) ListTelemetryObservations(c *gin.Context) {
	query, ok := parseCodexTelemetryObservationQuery(c)
	if !ok {
		return
	}
	if h.codexTelemetry == nil {
		response.Error(c, http.StatusServiceUnavailable, "Codex telemetry service is unavailable")
		return
	}
	response.Success(c, h.codexTelemetry.Observations(query))
}

func parseCodexTelemetryObservationQuery(c *gin.Context) (service.CodexTelemetryObservationQuery, bool) {
	query := service.CodexTelemetryObservationQuery{Page: 1, PageSize: 20}
	for name, target := range map[string]*int{"page": &query.Page, "page_size": &query.PageSize} {
		if raw, present := c.GetQuery(name); present {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || (name == "page_size" && value > 100) || (name == "page" && value > 1000000) {
				response.Error(c, http.StatusBadRequest, "Invalid "+name)
				return query, false
			}
			*target = value
		}
	}
	if raw, present := c.GetQuery("account_id"); present {
		accountID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || accountID <= 0 {
			response.Error(c, http.StatusBadRequest, "Invalid account_id")
			return query, false
		}
		query.AccountID = accountID
	}
	query.Status = c.Query("status")
	switch query.Status {
	case "", "queued", "sent", "failed", "dropped", "cancelled", "skipped":
	default:
		response.Error(c, http.StatusBadRequest, "Invalid telemetry status")
		return query, false
	}
	query.Type = c.Query("type")
	switch query.Type {
	case "", "analytics", "metrics":
	default:
		response.Error(c, http.StatusBadRequest, "Invalid telemetry type")
		return query, false
	}
	return query, true
}
