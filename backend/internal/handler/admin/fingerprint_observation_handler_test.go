package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestListFingerprintObservationsDisabledReturnsEmptyClampedPage(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return false }
	createCalls := 0
	fingerprintObservationServiceAPI.create = func() (string, error) {
		createCalls++
		return "unexpected", nil
	}

	recorder := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations?page=30&page_size=500",
		(&OpenAIOAuthHandler{}).ListFingerprintObservations,
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Code int                             `json:"code"`
		Data fingerprintObservationsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code)
	require.False(t, envelope.Data.Enabled)
	require.Empty(t, envelope.Data.SnapshotToken)
	require.Empty(t, envelope.Data.Items)
	require.Equal(t, 0, envelope.Data.Total)
	require.Equal(t, 1, envelope.Data.Page)
	require.Equal(t, fingerprintObservationMaxPageSize, envelope.Data.PageSize)
	require.Equal(t, 1, envelope.Data.Pages)
	require.Equal(t, 0, createCalls, "disabled observation must not retain a snapshot")
}

func TestListFingerprintObservationsCreatesSnapshotAndClampsUpperPage(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }
	createCalls := 0
	fingerprintObservationServiceAPI.create = func() (string, error) {
		createCalls++
		return "snapshot-new", nil
	}
	type usersCall struct {
		token    string
		page     int
		pageSize int
	}
	var calls []usersCall
	fingerprintObservationServiceAPI.users = func(token string, page, pageSize int) (service.FingerprintObservationUserPage, error) {
		calls = append(calls, usersCall{token: token, page: page, pageSize: pageSize})
		items := []service.FingerprintObservationUserSummary{}
		if page == 3 {
			items = make([]service.FingerprintObservationUserSummary, 5)
		}
		return service.FingerprintObservationUserPage{
			SnapshotToken: token,
			Items:         items,
			Total:         205,
			Page:          page,
			PageSize:      pageSize,
			Pages:         3,
		}, nil
	}

	recorder := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations?page=99&page_size=500",
		(&OpenAIOAuthHandler{}).ListFingerprintObservations,
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data fingerprintObservationsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.True(t, envelope.Data.Enabled)
	require.Equal(t, "snapshot-new", envelope.Data.SnapshotToken)
	require.Len(t, envelope.Data.Items, 5)
	require.Equal(t, 205, envelope.Data.Total)
	require.Equal(t, 3, envelope.Data.Page)
	require.Equal(t, 100, envelope.Data.PageSize)
	require.Equal(t, 3, envelope.Data.Pages)
	require.Equal(t, 1, createCalls)
	require.Equal(t, []usersCall{
		{token: "snapshot-new", page: 99, pageSize: 100},
		{token: "snapshot-new", page: 3, pageSize: 100},
	}, calls)
}

func TestListFingerprintObservationsReusesSnapshotToken(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }
	fingerprintObservationServiceAPI.create = func() (string, error) {
		t.Fatal("an existing snapshot token must not create a new snapshot")
		return "", nil
	}
	fingerprintObservationServiceAPI.users = func(token string, page, pageSize int) (service.FingerprintObservationUserPage, error) {
		require.Equal(t, "snapshot-existing", token)
		require.Equal(t, 2, page)
		require.Equal(t, 10, pageSize)
		return service.FingerprintObservationUserPage{
			SnapshotToken: token,
			Items:         []service.FingerprintObservationUserSummary{},
			Total:         15,
			Page:          page,
			PageSize:      pageSize,
			Pages:         2,
		}, nil
	}

	recorder := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations?snapshot_token=snapshot-existing&page=2&page_size=10",
		(&OpenAIOAuthHandler{}).ListFingerprintObservations,
	)
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestListFingerprintObservationsMapsExpiredSnapshotToConflict(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }
	fingerprintObservationServiceAPI.users = func(string, int, int) (service.FingerprintObservationUserPage, error) {
		return service.FingerprintObservationUserPage{}, service.ErrFingerprintObservationSnapshotNotFound
	}

	recorder := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations?snapshot_token=expired",
		(&OpenAIOAuthHandler{}).ListFingerprintObservations,
	)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, fingerprintSnapshotExpiredReason, decodeFingerprintObservationErrorReason(t, recorder))
}

func TestFingerprintObservationChildQueryValidation(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }

	for _, path := range []string{
		"/api/v1/admin/openai/fingerprint-observations/api-keys",
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot",
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot&parent_node_id=user&cursor=%20%20",
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot&parent_node_id=user&limit=bad",
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot&parent_node_id=user&limit=0",
	} {
		recorder := invokeFingerprintObservationHandler(t, path, (&OpenAIOAuthHandler{}).ListFingerprintObservationAPIKeys)
		require.Equal(t, http.StatusBadRequest, recorder.Code, path)
	}
}

func TestFingerprintObservationChildPaginationBounds(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }
	var limits []int
	fingerprintObservationServiceAPI.apiKeys = func(token, parent, cursor string, limit int) (service.FingerprintObservationAPIKeyPage, error) {
		require.Equal(t, "snapshot", token)
		require.Equal(t, "user-node", parent)
		limits = append(limits, limit)
		return service.FingerprintObservationAPIKeyPage{
			Items:      []service.FingerprintObservationAPIKeySummary{},
			Total:      25,
			NextCursor: "next",
		}, nil
	}

	handler := (&OpenAIOAuthHandler{}).ListFingerprintObservationAPIKeys
	first := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot&parent_node_id=user-node&limit=500",
		handler,
	)
	second := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot&parent_node_id=user-node",
		handler,
	)

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, []int{100, 20}, limits)
	var envelope struct {
		Data service.FingerprintObservationAPIKeyPage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &envelope))
	require.Equal(t, 25, envelope.Data.Total)
	require.Equal(t, "next", envelope.Data.NextCursor)
}

func TestFingerprintObservationChildHandlersReturnTypedPages(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }
	fingerprintObservationServiceAPI.sessions = func(token, parent, cursor string, limit int) (service.FingerprintObservationSessionPage, error) {
		requireFingerprintObservationChildArgs(t, token, parent, cursor, limit)
		return service.FingerprintObservationSessionPage{Items: []service.FingerprintObservationSessionSummary{}, Total: 2, NextCursor: "session-next"}, nil
	}
	fingerprintObservationServiceAPI.threads = func(token, parent, cursor string, limit int) (service.FingerprintObservationThreadPage, error) {
		requireFingerprintObservationChildArgs(t, token, parent, cursor, limit)
		return service.FingerprintObservationThreadPage{Items: []service.FingerprintObservationThreadSummary{}, Total: 3, NextCursor: "thread-next"}, nil
	}
	fingerprintObservationServiceAPI.entries = func(token, parent, cursor string, limit int) (service.FingerprintObservationEntryPage, error) {
		requireFingerprintObservationChildArgs(t, token, parent, cursor, limit)
		return service.FingerprintObservationEntryPage{Items: []service.FingerprintObservationEntry{}, Total: 4, NextCursor: "entry-next"}, nil
	}

	for _, tc := range []struct {
		name       string
		handler    gin.HandlerFunc
		wantTotal  int
		wantCursor string
	}{
		{name: "sessions", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationSessions, wantTotal: 2, wantCursor: "session-next"},
		{name: "threads", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationThreads, wantTotal: 3, wantCursor: "thread-next"},
		{name: "entries", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationEntries, wantTotal: 4, wantCursor: "entry-next"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := invokeFingerprintObservationHandler(t,
				"/api/v1/admin/openai/fingerprint-observations/"+tc.name+"?snapshot_token=snapshot&parent_node_id=parent&cursor=cursor&limit=7",
				tc.handler,
			)
			require.Equal(t, http.StatusOK, recorder.Code)
			var envelope struct {
				Data struct {
					Total      int    `json:"total"`
					NextCursor string `json:"next_cursor"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.Equal(t, tc.wantTotal, envelope.Data.Total)
			require.Equal(t, tc.wantCursor, envelope.Data.NextCursor)
		})
	}
}

func TestFingerprintObservationChildServiceErrors(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return true }
	fingerprintObservationServiceAPI.apiKeys = func(string, string, string, int) (service.FingerprintObservationAPIKeyPage, error) {
		return service.FingerprintObservationAPIKeyPage{}, service.ErrFingerprintObservationSnapshotNotFound
	}
	fingerprintObservationServiceAPI.sessions = func(string, string, string, int) (service.FingerprintObservationSessionPage, error) {
		return service.FingerprintObservationSessionPage{}, service.ErrFingerprintObservationCursorInvalid
	}
	fingerprintObservationServiceAPI.threads = func(string, string, string, int) (service.FingerprintObservationThreadPage, error) {
		return service.FingerprintObservationThreadPage{}, service.ErrFingerprintObservationNodeNotFound
	}
	fingerprintObservationServiceAPI.entries = func(string, string, string, int) (service.FingerprintObservationEntryPage, error) {
		return service.FingerprintObservationEntryPage{}, errors.New("store failure")
	}

	for _, tc := range []struct {
		name       string
		handler    gin.HandlerFunc
		wantStatus int
		wantReason string
	}{
		{name: "api-keys", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationAPIKeys, wantStatus: http.StatusConflict, wantReason: fingerprintSnapshotExpiredReason},
		{name: "sessions", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationSessions, wantStatus: http.StatusBadRequest, wantReason: "fingerprint_observation_cursor_invalid"},
		{name: "threads", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationThreads, wantStatus: http.StatusNotFound, wantReason: "fingerprint_observation_node_not_found"},
		{name: "entries", handler: (&OpenAIOAuthHandler{}).ListFingerprintObservationEntries, wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := invokeFingerprintObservationHandler(t,
				"/api/v1/admin/openai/fingerprint-observations/"+tc.name+"?snapshot_token=snapshot&parent_node_id=parent",
				tc.handler,
			)
			require.Equal(t, tc.wantStatus, recorder.Code)
			if tc.wantReason != "" {
				require.Equal(t, tc.wantReason, decodeFingerprintObservationErrorReason(t, recorder))
			}
		})
	}
}

func TestFingerprintObservationChildDisabledReturnsExplicitConflict(t *testing.T) {
	restoreFingerprintObservationServiceAPI(t)
	fingerprintObservationServiceAPI.enabled = func() bool { return false }

	recorder := invokeFingerprintObservationHandler(t,
		"/api/v1/admin/openai/fingerprint-observations/api-keys?snapshot_token=snapshot&parent_node_id=user",
		(&OpenAIOAuthHandler{}).ListFingerprintObservationAPIKeys,
	)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, fingerprintSnapshotExpiredReason, decodeFingerprintObservationErrorReason(t, recorder))
}

func restoreFingerprintObservationServiceAPI(t *testing.T) {
	t.Helper()
	original := fingerprintObservationServiceAPI
	t.Cleanup(func() { fingerprintObservationServiceAPI = original })
}

func invokeFingerprintObservationHandler(t *testing.T, target string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	handler(c)
	return recorder
}

func decodeFingerprintObservationErrorReason(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope.Reason
}

func requireFingerprintObservationChildArgs(t *testing.T, token, parent, cursor string, limit int) {
	t.Helper()
	require.Equal(t, "snapshot", token)
	require.Equal(t, "parent", parent)
	require.Equal(t, "cursor", cursor)
	require.Equal(t, 7, limit)
}
