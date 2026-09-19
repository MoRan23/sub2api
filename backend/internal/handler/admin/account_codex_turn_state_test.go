package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexTurnStateHandlerTestService struct {
	ids        []int64
	batchCalls int
	singleID   int64
	result     *service.CodexTurnStateBatchStatus
	err        error
}

func (s *codexTurnStateHandlerTestService) GetStatus(_ context.Context, id int64) (*service.CodexTurnStateStatus, error) {
	s.singleID = id
	return &service.CodexTurnStateStatus{AccountID: id}, s.err
}

func (s *codexTurnStateHandlerTestService) GetStatuses(_ context.Context, ids []int64) (*service.CodexTurnStateBatchStatus, error) {
	s.batchCalls++
	s.ids = append([]int64(nil), ids...)
	return s.result, s.err
}

func codexTurnStateBatchHandlerRequest(t *testing.T, h *AccountHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/accounts/codex-turn-state"+query, nil)
	h.GetCodexTurnStates(c)
	return recorder
}

func TestAccountCodexTurnStateBatchRejectsInvalidIDsWithoutService(t *testing.T) {
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = strconv.Itoa(i + 1)
	}
	cases := map[string]string{
		"missing":          "",
		"empty":            "?account_ids=",
		"empty_first":      "?account_ids=%2C1",
		"empty_last":       "?account_ids=1%2C",
		"empty_middle":     "?account_ids=1%2C%2C2",
		"zero":             "?account_ids=0",
		"negative":         "?account_ids=-1",
		"non_numeric":      "?account_ids=1%2Cabc",
		"decimal":          "?account_ids=1.2",
		"whitespace":       "?account_ids=1%2C%202",
		"signed":           "?account_ids=%2B1",
		"overflow":         "?account_ids=9223372036854775808",
		"multiple_queries": "?account_ids=1&account_ids=2",
		"too_many":         "?account_ids=" + url.QueryEscape(strings.Join(tooMany, ",")),
		"too_long":         "?account_ids=" + strings.Repeat("1", 8193),
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			state := &codexTurnStateHandlerTestService{}
			h := &AccountHandler{}
			h.SetCodexTurnStateService(state)
			recorder := codexTurnStateBatchHandlerRequest(t, h, query)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Zero(t, state.batchCalls)
			require.Zero(t, state.singleID)
		})
	}
}

func TestAccountCodexTurnStateBatchDeduplicatesAndKeepsReturnedShape(t *testing.T) {
	state := &codexTurnStateHandlerTestService{result: &service.CodexTurnStateBatchStatus{
		Items:  map[string]*service.CodexTurnStateStatus{"2": {AccountID: 2, OwnerAccountID: 7, Inherited: true}},
		Models: []string{"z-final", "a-final"},
	}}
	h := &AccountHandler{}
	h.SetCodexTurnStateService(state)
	recorder := codexTurnStateBatchHandlerRequest(t, h, "?account_ids=2,1,2,0001,3")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []int64{2, 1, 3}, state.ids)
	require.Equal(t, 1, state.batchCalls)
	var body struct {
		Data service.CodexTurnStateBatchStatus `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, []string{"z-final", "a-final"}, body.Data.Models)
	require.Len(t, body.Data.Items, 1)
	require.Equal(t, int64(7), body.Data.Items["2"].OwnerAccountID)
	require.NotContains(t, body.Data.Items, "1", "missing service keys must remain absent")
	require.NotContains(t, body.Data.Items, "3")
}

func TestAccountCodexTurnStateBatchAccepts200LongIDsAndManyDuplicates(t *testing.T) {
	for _, duplicated := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicated_%t", duplicated), func(t *testing.T) {
			ids := make([]int64, 200)
			raw := make([]string, 200)
			for i := range ids {
				ids[i] = int64(9223372036854775807) - int64(i)
				raw[i] = strconv.FormatInt(ids[i], 10)
			}
			if duplicated {
				raw = append(raw, raw...)
			}
			state := &codexTurnStateHandlerTestService{result: &service.CodexTurnStateBatchStatus{Items: map[string]*service.CodexTurnStateStatus{}, Models: []string{}}}
			h := &AccountHandler{}
			h.SetCodexTurnStateService(state)
			recorder := codexTurnStateBatchHandlerRequest(t, h, "?account_ids="+url.QueryEscape(strings.Join(raw, ",")))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, ids, state.ids)
			require.Equal(t, 1, state.batchCalls)
		})
	}
}

func TestAccountCodexTurnStateBatchUnavailableAndServiceErrors(t *testing.T) {
	recorder := codexTurnStateBatchHandlerRequest(t, &AccountHandler{}, "?account_ids=1")
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "CODEX_TURN_STATE_UNAVAILABLE")
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"internal", errors.New("storage failed"), http.StatusInternalServerError},
		{"typed", infraerrors.New(503, "TEST_STORAGE_DOWN", "storage unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &codexTurnStateHandlerTestService{err: tc.err}
			h := &AccountHandler{}
			h.SetCodexTurnStateService(state)
			recorder := codexTurnStateBatchHandlerRequest(t, h, "?account_ids=1")
			require.Equal(t, tc.status, recorder.Code)
			require.Equal(t, 1, state.batchCalls)
		})
	}
}

func TestAccountCodexTurnStateSingleIDRemainsCompatible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := &codexTurnStateHandlerTestService{}
	h := &AccountHandler{}
	h.SetCodexTurnStateService(state)
	for _, id := range []string{"7", "bad", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/admin/accounts/"+id+"/codex-turn-state", nil)
			c.Params = gin.Params{{Key: "id", Value: id}}
			h.GetCodexTurnState(c)
			if id == "7" {
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, int64(7), state.singleID)
			} else {
				require.Equal(t, http.StatusBadRequest, recorder.Code)
			}
			require.Zero(t, state.batchCalls)
		})
	}
}
