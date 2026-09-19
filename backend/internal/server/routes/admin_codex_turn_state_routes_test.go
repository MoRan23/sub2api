package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexTurnStateRouteTestService struct {
	batchIDs []int64
	singleID int64
}

func (s *codexTurnStateRouteTestService) GetStatus(_ context.Context, id int64) (*service.CodexTurnStateStatus, error) {
	s.singleID = id
	return &service.CodexTurnStateStatus{AccountID: id}, nil
}

func (s *codexTurnStateRouteTestService) GetStatuses(_ context.Context, ids []int64) (*service.CodexTurnStateBatchStatus, error) {
	s.batchIDs = append([]int64(nil), ids...)
	return &service.CodexTurnStateBatchStatus{Items: map[string]*service.CodexTurnStateStatus{}, Models: []string{}}, nil
}

func TestAccountCodexTurnStateRoutesDispatchStaticAndSingleID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	state := &codexTurnStateRouteTestService{}
	account := &adminhandler.AccountHandler{}
	account.SetCodexTurnStateService(state)
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: account}}
	stepUpCalls := 0
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusPreconditionRequired)
	})
	registerAccountRoutes(router.Group("/admin"), handlers, stepUp)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/codex-turn-state?account_ids=2,1", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []int64{2, 1}, state.batchIDs)
	require.Zero(t, state.singleID, "the static route must not be treated as an account ID")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/8/codex-turn-state", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, int64(8), state.singleID)
	require.Zero(t, stepUpCalls, "read-only status routes do not require step-up authorization")
}
