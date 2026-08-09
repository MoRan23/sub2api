package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	fingerprintObservationDefaultPageSize = 20
	fingerprintObservationMaxPageSize     = 100
	fingerprintSnapshotExpiredReason      = "fingerprint_snapshot_expired"
)

// The narrow function table keeps the HTTP contract independently testable
// while production continues to use the process-local observation service.
var fingerprintObservationServiceAPI = struct {
	enabled  func() bool
	create   func() (string, error)
	users    func(string, int, int) (service.FingerprintObservationUserPage, error)
	apiKeys  func(string, string, string, int) (service.FingerprintObservationAPIKeyPage, error)
	sessions func(string, string, string, int) (service.FingerprintObservationSessionPage, error)
	threads  func(string, string, string, int) (service.FingerprintObservationThreadPage, error)
	entries  func(string, string, string, int) (service.FingerprintObservationEntryPage, error)
}{
	enabled:  service.IsFingerprintObservationEnabled,
	create:   service.CreateFingerprintObservationSnapshot,
	users:    service.PageFingerprintObservationUsers,
	apiKeys:  service.ListFingerprintObservationAPIKeys,
	sessions: service.ListFingerprintObservationSessions,
	threads:  service.ListFingerprintObservationThreads,
	entries:  service.ListFingerprintObservationEntries,
}

type fingerprintObservationsResponse struct {
	Enabled       bool                                        `json:"enabled"`
	SnapshotToken string                                      `json:"snapshot_token"`
	Items         []service.FingerprintObservationUserSummary `json:"items"`
	Total         int                                         `json:"total"`
	Page          int                                         `json:"page"`
	PageSize      int                                         `json:"page_size"`
	Pages         int                                         `json:"pages"`
}

// ListFingerprintObservations returns a page of users from an immutable,
// short-lived observation snapshot. Omitting snapshot_token starts a fresh
// snapshot; subsequent pages must reuse the returned token.
// GET /api/v1/admin/openai/fingerprint-observations
func (h *OpenAIOAuthHandler) ListFingerprintObservations(c *gin.Context) {
	page := parseFingerprintObservationPage(c.Query("page"))
	pageSize := parseFingerprintObservationPageSize(c.Query("page_size"))

	if !fingerprintObservationServiceAPI.enabled() {
		writeDisabledFingerprintObservationPage(c, pageSize)
		return
	}

	snapshotToken := strings.TrimSpace(c.Query("snapshot_token"))
	if snapshotToken == "" {
		var err error
		snapshotToken, err = fingerprintObservationServiceAPI.create()
		if err != nil {
			writeFingerprintObservationServiceError(c, err)
			return
		}
	}

	result, err := fingerprintObservationServiceAPI.users(snapshotToken, page, pageSize)
	if err != nil {
		// Disabling observation invalidates every snapshot. If that races this
		// request, the top-level endpoint still reports its disabled empty state.
		if errors.Is(err, service.ErrFingerprintObservationSnapshotNotFound) && !fingerprintObservationServiceAPI.enabled() {
			writeDisabledFingerprintObservationPage(c, pageSize)
			return
		}
		writeFingerprintObservationServiceError(c, err)
		return
	}

	// The service normalizes lower bounds and page size. Clamp the upper page
	// bound here so the public contract never returns an empty phantom page.
	if result.Pages < 1 {
		result.Pages = 1
	}
	if result.Page > result.Pages {
		result, err = fingerprintObservationServiceAPI.users(snapshotToken, result.Pages, pageSize)
		if err != nil {
			writeFingerprintObservationServiceError(c, err)
			return
		}
	}

	response.Success(c, fingerprintObservationsResponse{
		Enabled:       true,
		SnapshotToken: result.SnapshotToken,
		Items:         result.Items,
		Total:         result.Total,
		Page:          result.Page,
		PageSize:      result.PageSize,
		Pages:         result.Pages,
	})
}

// ListFingerprintObservationAPIKeys lazily returns API-key summaries for one
// user node in the selected snapshot.
// GET /api/v1/admin/openai/fingerprint-observations/api-keys
func (h *OpenAIOAuthHandler) ListFingerprintObservationAPIKeys(c *gin.Context) {
	query, ok := parseFingerprintObservationChildQuery(c)
	if !ok {
		return
	}
	if !ensureFingerprintObservationAvailable(c) {
		return
	}
	result, err := fingerprintObservationServiceAPI.apiKeys(query.snapshotToken, query.parentNodeID, query.cursor, query.limit)
	if err != nil {
		writeFingerprintObservationServiceError(c, err)
		return
	}
	response.Success(c, result)
}

// ListFingerprintObservationSessions lazily returns root sessions for one API
// key node in the selected snapshot.
// GET /api/v1/admin/openai/fingerprint-observations/sessions
func (h *OpenAIOAuthHandler) ListFingerprintObservationSessions(c *gin.Context) {
	query, ok := parseFingerprintObservationChildQuery(c)
	if !ok {
		return
	}
	if !ensureFingerprintObservationAvailable(c) {
		return
	}
	result, err := fingerprintObservationServiceAPI.sessions(query.snapshotToken, query.parentNodeID, query.cursor, query.limit)
	if err != nil {
		writeFingerprintObservationServiceError(c, err)
		return
	}
	response.Success(c, result)
}

// ListFingerprintObservationThreads lazily returns thread summaries for one
// root session node in the selected snapshot.
// GET /api/v1/admin/openai/fingerprint-observations/threads
func (h *OpenAIOAuthHandler) ListFingerprintObservationThreads(c *gin.Context) {
	query, ok := parseFingerprintObservationChildQuery(c)
	if !ok {
		return
	}
	if !ensureFingerprintObservationAvailable(c) {
		return
	}
	result, err := fingerprintObservationServiceAPI.threads(query.snapshotToken, query.parentNodeID, query.cursor, query.limit)
	if err != nil {
		writeFingerprintObservationServiceError(c, err)
		return
	}
	response.Success(c, result)
}

// ListFingerprintObservationEntries lazily returns final wire observations for
// one thread (or the explicit unthreaded branch) in the selected snapshot.
// GET /api/v1/admin/openai/fingerprint-observations/entries
func (h *OpenAIOAuthHandler) ListFingerprintObservationEntries(c *gin.Context) {
	query, ok := parseFingerprintObservationChildQuery(c)
	if !ok {
		return
	}
	if !ensureFingerprintObservationAvailable(c) {
		return
	}
	result, err := fingerprintObservationServiceAPI.entries(query.snapshotToken, query.parentNodeID, query.cursor, query.limit)
	if err != nil {
		writeFingerprintObservationServiceError(c, err)
		return
	}
	response.Success(c, result)
}

type fingerprintObservationChildQuery struct {
	snapshotToken string
	parentNodeID  string
	cursor        string
	limit         int
}

func parseFingerprintObservationChildQuery(c *gin.Context) (fingerprintObservationChildQuery, bool) {
	query := fingerprintObservationChildQuery{
		snapshotToken: strings.TrimSpace(c.Query("snapshot_token")),
		parentNodeID:  strings.TrimSpace(c.Query("parent_node_id")),
		cursor:        strings.TrimSpace(c.Query("cursor")),
		limit:         fingerprintObservationDefaultPageSize,
	}
	if query.snapshotToken == "" {
		response.BadRequest(c, "snapshot_token is required")
		return fingerprintObservationChildQuery{}, false
	}
	if query.parentNodeID == "" {
		response.BadRequest(c, "parent_node_id is required")
		return fingerprintObservationChildQuery{}, false
	}
	if raw, exists := c.GetQuery("cursor"); exists && raw != "" && query.cursor == "" {
		response.BadRequest(c, "cursor is invalid")
		return fingerprintObservationChildQuery{}, false
	}
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "limit must be a positive integer")
			return fingerprintObservationChildQuery{}, false
		}
		query.limit = parsed
		if query.limit > fingerprintObservationMaxPageSize {
			query.limit = fingerprintObservationMaxPageSize
		}
	}
	return query, true
}

func parseFingerprintObservationPage(raw string) int {
	parsed, err := strconv.Atoi(raw)
	if raw == "" || err != nil || parsed < 1 {
		return 1
	}
	return parsed
}

func parseFingerprintObservationPageSize(raw string) int {
	parsed, err := strconv.Atoi(raw)
	if raw == "" || err != nil || parsed < 1 {
		return fingerprintObservationDefaultPageSize
	}
	if parsed > fingerprintObservationMaxPageSize {
		return fingerprintObservationMaxPageSize
	}
	return parsed
}

func ensureFingerprintObservationAvailable(c *gin.Context) bool {
	if fingerprintObservationServiceAPI.enabled() {
		return true
	}
	response.ErrorWithDetails(c, http.StatusConflict,
		"Fingerprint observation snapshot is unavailable",
		fingerprintSnapshotExpiredReason, nil)
	return false
}

func writeDisabledFingerprintObservationPage(c *gin.Context, pageSize int) {
	response.Success(c, fingerprintObservationsResponse{
		Enabled:       false,
		SnapshotToken: "",
		Items:         []service.FingerprintObservationUserSummary{},
		Total:         0,
		Page:          1,
		PageSize:      pageSize,
		Pages:         1,
	})
}

func writeFingerprintObservationServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrFingerprintObservationSnapshotNotFound):
		response.ErrorWithDetails(c, http.StatusConflict,
			"Fingerprint observation snapshot has expired",
			fingerprintSnapshotExpiredReason, nil)
	case errors.Is(err, service.ErrFingerprintObservationCursorInvalid):
		response.ErrorWithDetails(c, http.StatusBadRequest,
			"Fingerprint observation cursor is invalid",
			"fingerprint_observation_cursor_invalid", nil)
	case errors.Is(err, service.ErrFingerprintObservationNodeNotFound):
		response.ErrorWithDetails(c, http.StatusNotFound,
			"Fingerprint observation node was not found",
			"fingerprint_observation_node_not_found", nil)
	default:
		response.InternalError(c, "Failed to load fingerprint observations")
	}
}
