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

const candyQueueAnswer = `<!DOCTYPE html><html><body><svg></svg></body></html>`

const candyIndeterminateAnswer = "|问题|最少数量|最优取法（简洁）|\n|---|---|---|\n|第1问：固定取法|无法唯一确定|按不同解释可得32颗。|\n|第2问：自适应取法|无法唯一确定|题目未说明观察条件。|\n|第3问：固定取法|无法唯一确定|同第1问。|\n|第3问：自适应取法|无法唯一确定|不同解释下最少数量会不同。|"

func TestCandyQueueExecutionClassifiesOnlyCompleteAnswers(t *testing.T) {
	cases := []struct {
		name, text   string
		completed    bool
		err          error
		status, code string
	}{
		{"correct", candyQueueAnswer, true, nil, "generated", ""},
		{"fenced", "```html\n" + candyQueueAnswer + "```", true, nil, "generated", ""},
		{"missing", "|问题|最少数量|\n|第1问|32|", true, nil, "abnormal", "missing_html"},
		{"indeterminate", candyIndeterminateAnswer, true, nil, "abnormal", "missing_html"},
		{"conflicting", candyQueueAnswer + "<html><body>different</body></html>", true, nil, "abnormal", "ambiguous_html"},
		{"refusal", "题目信息不足，无法给出四项最少数量。", true, nil, "abnormal", "missing_html"},
		{"incomplete", candyQueueAnswer, false, nil, "failed", "missing_terminal"},
		{"incomplete_indeterminate", candyIndeterminateAnswer, false, nil, "failed", "missing_terminal"},
		{"transport", candyQueueAnswer, true, errors.New("secret upstream credential"), "failed", "execution_failed"},
		{"transport_indeterminate", candyIndeterminateAnswer, true, errors.New("synthetic stream failure"), "failed", "execution_failed"},
		{"authorization_changed", candyQueueAnswer, true, candyTestError("authorization_changed"), "failed", "authorization_changed"},
		{"rate_limited", "", false, ErrDiagnosticRateLimited, "skipped", "account_rate_limited"},
		{"429", "", false, candyTestError("upstream_http_429"), "skipped", "account_rate_limited"},
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
			} else {
				require.Equal(t, tt.text, repo.completed.ResponseText)
			}
			require.Empty(t, repo.completed.Answers, "pelican tests never grade numbers")
			if tt.status == "generated" {
				require.Equal(t, candyQueueAnswer, repo.completed.HTML)
			} else {
				require.Empty(t, repo.completed.HTML)
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

func TestCandyQueueUnresolvedAnswersAreAbnormalRegardlessOfWording(t *testing.T) {
	for _, answer := range []string{"未确定", "无法唯一确定", "无法判断", "不知道", "条件不足", "待定", "N/A", "unknown", "not enough information", "—"} {
		t.Run(answer, func(t *testing.T) {
			repo := &candyQueueRepoStub{}
			text := strings.ReplaceAll(candyIndeterminateAnswer, "无法唯一确定", answer)
			executor := &candyQueueExecutorStub{execute: func(context.Context, *CandyTestItem) (*CandyTestExecution, error) {
				return &CandyTestExecution{ResponseText: text, Completed: true}, nil
			}}
			s := NewAccountCandyTestService(repo, executor)
			defer s.Stop()
			s.execute(&CandyTestItem{ID: 1, ClaimID: "claim"})
			require.Equal(t, "abnormal", repo.completed.Status)
			require.Equal(t, "missing_html", repo.completed.FailureCode)
			require.Equal(t, text, repo.completed.ResponseText)
			require.Empty(t, repo.completed.Answers)
			require.Equal(t, 1, executor.calls)
		})
	}
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
