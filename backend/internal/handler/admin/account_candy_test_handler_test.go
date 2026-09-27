package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type candyHandlerRepo struct {
	service.CandyTestRepository
	created   *service.CandyTestCreateRequest
	cancelled []int64
	err       error
}

func (r *candyHandlerRepo) Create(_ context.Context, request *service.CandyTestCreateRequest, items []*service.CandyTestItem) (*service.CandyTestBatch, error) {
	r.created = request
	return &service.CandyTestBatch{ID: "00000000-0000-4000-8000-000000000001", Items: items}, r.err
}
func (r *candyHandlerRepo) History(context.Context, int64) ([]*service.CandyTestItem, error) {
	return []*service.CandyTestItem{{ID: 1, Status: "normal", ResponseText: "test answer"}}, r.err
}
func (r *candyHandlerRepo) Summaries(context.Context, []int64) (map[int64]*service.CandyTestSummary, error) {
	return map[int64]*service.CandyTestSummary{8: {Active: &service.CandyTestItem{ID: 2, BatchID: "active", Status: "running"}}}, r.err
}
func (r *candyHandlerRepo) Cancel(_ context.Context, _ string, ids []int64) error {
	r.cancelled = ids
	return r.err
}

type candyHandlerExecutor struct{ service.CandyTestExecutor }

func (*candyHandlerExecutor) Options(context.Context, []int64) (*service.CandyTestOptions, error) {
	return &service.CandyTestOptions{Accounts: []service.CandyTestAccountOptions{{AccountID: 8, Models: []service.CandyTestModelOption{{ID: "test-model", ReasoningEfforts: []string{"high"}}}}}}, nil
}
func candyHandlerContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}
func TestCandyHandlerCreateAndHistory(t *testing.T) {
	repo := &candyHandlerRepo{}
	h := &AccountHandler{}
	h.SetCandyTestService(service.NewAccountCandyTestService(repo, &candyHandlerExecutor{}))
	c, recorder := candyHandlerContext(t, `{"account_ids":[8,8],"model":"test-model","reasoning_effort":"high","idempotency_key":"one"}`)
	h.CreateCandyTests(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []int64{8}, repo.created.AccountIDs)
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	c, recorder = candyHandlerContext(t, "")
	c.Params = gin.Params{{Key: "id", Value: "8"}}
	h.GetCandyTestHistory(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"active"`)
	require.Contains(t, recorder.Body.String(), `"response_text":"test answer"`)
}
func TestCandyHandlerRejectsInvalidAndSanitizesErrors(t *testing.T) {
	repo := &candyHandlerRepo{err: errors.New("secret upstream token")}
	h := &AccountHandler{}
	h.SetCandyTestService(service.NewAccountCandyTestService(repo, &candyHandlerExecutor{}))
	c, recorder := candyHandlerContext(t, "")
	c.Params = gin.Params{{Key: "id", Value: "8"}}
	h.GetCandyTestHistory(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "secret")
	c, recorder = candyHandlerContext(t, `{"item_ids":[-1]}`)
	c.Params = gin.Params{{Key: "batch_id", Value: "00000000-0000-4000-8000-000000000001"}}
	h.CancelCandyTests(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Nil(t, repo.cancelled)
}
func TestCandySummaryChangesAccountListETag(t *testing.T) {
	items := []AccountListItemWithConcurrency{{CandyTest: &service.CandyTestSummary{Latest: &service.CandyTestItem{ID: 1, Status: "normal"}}}}
	before := buildAccountsListETag(items, 1, 1, 20, "openai", "", "", "", true)
	items[0].CandyTest.Latest.Status = "abnormal"
	after := buildAccountsListETag(items, 1, 1, 20, "openai", "", "", "", true)
	require.NotEqual(t, before, after)
}
