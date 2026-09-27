package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type candyQueueRepoStub struct {
	CandyTestRepository
	created   *CandyTestCreateRequest
	items     []*CandyTestItem
	completed *CandyTestItem
}

func (r *candyQueueRepoStub) Create(_ context.Context, request *CandyTestCreateRequest, items []*CandyTestItem) (*CandyTestBatch, error) {
	r.created = request
	r.items = items
	return &CandyTestBatch{Items: items}, nil
}
func (r *candyQueueRepoStub) Complete(_ context.Context, item *CandyTestItem) (bool, error) {
	r.completed = item
	return true, nil
}

type candyQueueExecutorStub struct {
	options *CandyTestOptions
	execute func(context.Context, *CandyTestItem) (*CandyTestExecution, error)
	calls   int
}

func (e *candyQueueExecutorStub) Options(context.Context, []int64) (*CandyTestOptions, error) {
	return e.options, nil
}
func (e *candyQueueExecutorStub) Execute(ctx context.Context, item *CandyTestItem) (*CandyTestExecution, error) {
	e.calls++
	return e.execute(ctx, item)
}

func TestCandyQueueCreateConfigurationAndExplicitSkips(t *testing.T) {
	repo := &candyQueueRepoStub{}
	executor := &candyQueueExecutorStub{options: &CandyTestOptions{Accounts: []CandyTestAccountOptions{
		{AccountID: 1, Models: []CandyTestModelOption{{ID: "fixture", ReasoningEfforts: []string{"high"}}}},
		{AccountID: 2, Models: []CandyTestModelOption{{ID: "other"}}},
		{AccountID: 3, Models: []CandyTestModelOption{{ID: "fixture"}}},
		{AccountID: 4, SkipReason: "unsupported_platform"},
	}}}
	s := NewAccountCandyTestService(repo, executor)
	defer s.Stop()
	original := &CandyTestCreateRequest{AccountIDs: []int64{4, 3, 2, 1, 1}, Model: "fixture", ReasoningEffort: "high", IdempotencyKey: "create-key"}
	_, err := s.Create(context.Background(), original)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4}, repo.created.AccountIDs)
	require.Equal(t, []int64{4, 3, 2, 1, 1}, original.AccountIDs, "normalization must not modify caller input")
	require.Equal(t, "queued", repo.items[0].Status)
	require.Equal(t, "unsupported_model", repo.items[1].FailureCode)
	require.Equal(t, "unsupported_reasoning_effort", repo.items[2].FailureCode)
	require.Equal(t, "unsupported_platform", repo.items[3].FailureCode)
	require.Zero(t, executor.calls, "creating a batch performs no inference")
}

func TestCandyQueueRejectsInvalidAndMissingAccounts(t *testing.T) {
	s := NewAccountCandyTestService(&candyQueueRepoStub{}, &candyQueueExecutorStub{options: &CandyTestOptions{}})
	defer s.Stop()
	for _, request := range []*CandyTestCreateRequest{nil, {}, {AccountIDs: []int64{-1}, Model: "a", IdempotencyKey: "x"}, {AccountIDs: []int64{1}, Model: "a", IdempotencyKey: "x"}} {
		_, err := s.Create(context.Background(), request)
		require.ErrorIs(t, err, ErrCandyTestInvalidRequest)
	}
}

const candyQueueAnswer = "|问题|最少数量|取法|\n|---|---|---|\n|第1问|32|说明|\n|第2问|29|说明|\n|第3问固定|40|说明|\n|第3问自适应|38|说明|"

func TestCandyQueueExecutionClassifiesOnlyCompleteAnswers(t *testing.T) {
	cases := []struct {
		name, text   string
		completed    bool
		err          error
		status, code string
	}{
		{"correct", candyQueueAnswer, true, nil, "normal", ""},
		{"wrong", strings.Replace(candyQueueAnswer, "|29|", "|30|", 1), true, nil, "abnormal", ""},
		{"missing", "|问题|最少数量|\n|第1问|32|", true, nil, "failed", "missing_answer"},
		{"incomplete", candyQueueAnswer, false, nil, "failed", "missing_terminal"},
		{"transport", candyQueueAnswer, true, errors.New("secret upstream credential"), "failed", "execution_failed"},
		{"size", strings.Repeat("x", CandyTestMaxResponseBytes+1), true, nil, "failed", "response_too_large"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := &candyQueueRepoStub{}
			executor := &candyQueueExecutorStub{execute: func(ctx context.Context, _ *CandyTestItem) (*CandyTestExecution, error) {
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.InDelta(t, CandyTestTimeout.Seconds(), time.Until(deadline).Seconds(), 1)
				return &CandyTestExecution{ResponseText: tt.text, Completed: tt.completed}, tt.err
			}}
			s := NewAccountCandyTestService(repo, executor)
			defer s.Stop()
			s.execute(&CandyTestItem{ID: 1, ClaimID: "claim"})
			require.Equal(t, tt.status, repo.completed.Status)
			require.Equal(t, tt.code, repo.completed.FailureCode)
			require.Equal(t, 1, executor.calls, "failed inference is never retried")
			if tt.name == "size" {
				require.Empty(t, repo.completed.ResponseText)
			}
		})
	}
}

func TestCandyQueueLateSuccessCannotOverrideDeadline(t *testing.T) {
	repo := &candyQueueRepoStub{}
	executor := &candyQueueExecutorStub{execute: func(ctx context.Context, _ *CandyTestItem) (*CandyTestExecution, error) {
		<-ctx.Done()
		return &CandyTestExecution{ResponseText: candyQueueAnswer, Completed: true}, nil
	}}
	s := NewAccountCandyTestService(repo, executor)
	defer s.Stop()
	started := time.Now().Add(-CandyTestTimeout - time.Second)
	s.execute(&CandyTestItem{ID: 1, ClaimID: "claim", StartedAt: &started})
	require.Equal(t, "failed", repo.completed.Status)
	require.Equal(t, "timeout", repo.completed.FailureCode)
	require.Empty(t, repo.completed.Answers)
}

func TestCandyQueueFailureCodesNeverStoreErrorText(t *testing.T) {
	require.Equal(t, "execution_failed", candySafeFailureCode("Authorization: secret", "execution_failed"))
	require.Equal(t, "http_401", candySafeFailureCode("http_401", "execution_failed"))
}

func TestCandyQueueExecutorPanicIsTerminalAndNotRetried(t *testing.T) {
	repo := &candyQueueRepoStub{}
	executor := &candyQueueExecutorStub{execute: func(context.Context, *CandyTestItem) (*CandyTestExecution, error) {
		panic("sensitive internal error")
	}}
	s := NewAccountCandyTestService(repo, executor)
	defer s.Stop()
	s.execute(&CandyTestItem{ID: 1, ClaimID: "claim"})
	require.Equal(t, "failed", repo.completed.Status)
	require.Equal(t, "execution_failed", repo.completed.FailureCode)
	require.Equal(t, 1, executor.calls)
}
