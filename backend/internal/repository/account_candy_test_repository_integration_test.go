//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func candyIntegrationAccount(t *testing.T) int64 {
	t.Helper()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "candy-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic"}})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, account.ID)
	})
	return account.ID
}

func candyIntegrationBatch(t *testing.T, repo service.CandyTestRepository, ids ...int64) *service.CandyTestBatch {
	t.Helper()
	items := make([]*service.CandyTestItem, len(ids))
	for i, id := range ids {
		items[i] = &service.CandyTestItem{AccountID: id, Status: "queued"}
	}
	b, err := repo.Create(context.Background(), &service.CandyTestCreateRequest{AccountIDs: ids, Model: "fixture", IdempotencyKey: uuid.NewString()}, items)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM account_candy_test_batches WHERE id=$1`, b.ID)
	})
	return b
}

func TestCandyRepositoryGlobalCapacityAndAccountSerialization(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	ids := []int64{candyIntegrationAccount(t), candyIntegrationAccount(t), candyIntegrationAccount(t), candyIntegrationAccount(t)}
	candyIntegrationBatch(t, repo, ids...)
	candyIntegrationBatch(t, repo, ids[0])
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(12)
	start := make(chan struct{})
	var mu sync.Mutex
	var claimed []*service.CandyTestItem
	var claimErrors []error
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			item, err := NewAccountCandyTestRepository(integrationDB).Claim(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				claimErrors = append(claimErrors, err)
			}
			if item != nil {
				claimed = append(claimed, item)
			}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	require.Empty(t, claimErrors)
	require.Len(t, claimed, 3)
	seen := map[int64]bool{}
	for _, item := range claimed {
		require.False(t, seen[item.AccountID])
		seen[item.AccountID] = true
	}
	summaries, err := repo.Summaries(ctx, []int64{ids[0]})
	require.NoError(t, err)
	require.Equal(t, "running", summaries[ids[0]].Active.Status, "a newer queued item must not hide the running test")
	owned, err := repo.Heartbeat(ctx, claimed[0].ID, uuid.NewString())
	require.NoError(t, err)
	require.False(t, owned)
	claimed[0].Status = "normal"
	claimed[0].Answers = map[string]int{"q1_fixed": 32}
	ok, err := repo.Complete(ctx, claimed[0])
	require.NoError(t, err)
	require.True(t, ok)
	item, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, item)
}

func TestCandyRepositoryQueuedSameAccountWaitsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	firstID, otherID := candyIntegrationAccount(t), candyIntegrationAccount(t)
	candyIntegrationBatch(t, repo, firstID)
	candyIntegrationBatch(t, repo, firstID)
	candyIntegrationBatch(t, repo, otherID)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var claimed []*service.CandyTestItem
	var claimErrors []error
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			item, err := NewAccountCandyTestRepository(integrationDB).Claim(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				claimErrors = append(claimErrors, err)
			}
			if item != nil {
				claimed = append(claimed, item)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Empty(t, claimErrors)
	require.Len(t, claimed, 2, "duplicate account remains queued despite the third global slot being free")
	for _, item := range claimed {
		if item.AccountID == firstID {
			item.Status = "failed"
			item.FailureCode = "fixture_failure"
			_, err := repo.Complete(ctx, item)
			require.NoError(t, err)
		}
	}
	next, err := NewAccountCandyTestRepository(integrationDB).Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, firstID, next.AccountID)
}

func TestCandyRepositoryCancellationLeaseExpiryAndLateCompletion(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	b := candyIntegrationBatch(t, repo, candyIntegrationAccount(t), candyIntegrationAccount(t))
	item, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.NoError(t, repo.Cancel(ctx, b.ID, nil))
	owned, err := repo.Heartbeat(ctx, item.ID, item.ClaimID)
	require.NoError(t, err)
	require.False(t, owned)
	item.Status = "normal"
	ok, err := repo.Complete(ctx, item)
	require.NoError(t, err)
	require.True(t, ok)
	finished, err := repo.GetBatch(ctx, b.ID, 1, 50)
	require.NoError(t, err)
	require.Equal(t, 2, finished.Counts["cancelled"])
	b2 := candyIntegrationBatch(t, repo, candyIntegrationAccount(t))
	item, err = repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, item)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_candy_test_items SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, item.ID)
	require.NoError(t, err)
	owned, err = repo.Heartbeat(ctx, item.ID, item.ClaimID)
	require.NoError(t, err)
	require.False(t, owned)
	item.Status = "normal"
	ok, err = repo.Complete(ctx, item)
	require.NoError(t, err)
	require.False(t, ok)
	next, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, next, "expired running work must not be replayed")
	failed, err := repo.GetBatch(ctx, b2.ID, 1, 50)
	require.NoError(t, err)
	require.Equal(t, "execution_interrupted", failed.Items[0].FailureCode)
}

func TestCandyRepositorySubsetCancellationIsBoundToBatch(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	batch := candyIntegrationBatch(t, repo, candyIntegrationAccount(t), candyIntegrationAccount(t))
	other := candyIntegrationBatch(t, repo, candyIntegrationAccount(t))
	require.NoError(t, repo.Cancel(ctx, batch.ID, []int64{batch.Items[0].ID, other.Items[0].ID}))
	updated, err := repo.GetBatch(ctx, batch.ID, 1, 50)
	require.NoError(t, err)
	require.Equal(t, "cancelled", updated.Items[0].Status)
	require.Equal(t, "queued", updated.Items[1].Status)
	unrelated, err := repo.GetBatch(ctx, other.ID, 1, 50)
	require.NoError(t, err)
	require.Equal(t, "queued", unrelated.Items[0].Status)
}

func TestCandyRepositoryIdempotencyAndFiveResultRetention(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	id := candyIntegrationAccount(t)
	request := &service.CandyTestCreateRequest{AccountIDs: []int64{id}, Model: "fixture", IdempotencyKey: uuid.NewString()}
	first, err := repo.Create(ctx, request, []*service.CandyTestItem{{AccountID: id, Status: "queued"}})
	require.NoError(t, err)
	again, err := repo.Create(ctx, request, []*service.CandyTestItem{{AccountID: id, Status: "queued"}})
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	request.ReasoningEffort = "high"
	_, err = repo.Create(ctx, request, nil)
	require.ErrorIs(t, err, service.ErrCandyTestIdempotencyConflict)
	for i := 0; i < 7; i++ {
		if i > 0 {
			candyIntegrationBatch(t, repo, id)
		}
		item, claimErr := repo.Claim(ctx)
		require.NoError(t, claimErr)
		require.NotNil(t, item)
		item.Status = "normal"
		item.ResponseText = "final answer fixture"
		_, err = repo.Complete(ctx, item)
		require.NoError(t, err)
	}
	history, err := repo.History(ctx, id)
	require.NoError(t, err)
	require.Len(t, history, 5)
	var n int
	err = integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_candy_test_items WHERE account_id=$1`, id).Scan(&n)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	candyIntegrationBatch(t, repo, id)
	summary, err := repo.Summaries(ctx, []int64{id})
	require.NoError(t, err)
	require.NotNil(t, summary[id].Latest)
	require.NotNil(t, summary[id].Active)
	require.Empty(t, summary[id].Latest.ResponseText)
	require.WithinDuration(t, time.Now(), summary[id].Latest.FinishedAt.UTC(), time.Minute)
}

func TestCandyRepositoryTimeoutWinsOverSuccess(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	b := candyIntegrationBatch(t, repo, candyIntegrationAccount(t))
	item, err := repo.Claim(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_candy_test_items SET started_at=NOW()-INTERVAL '21 minutes',lease_until=NOW()+INTERVAL '30 seconds' WHERE id=$1`, item.ID)
	require.NoError(t, err)
	item.Status = "normal"
	item.Answers = map[string]int{"q1_fixed": 32}
	ok, err := repo.Complete(ctx, item)
	require.NoError(t, err)
	require.True(t, ok)
	batch, err := repo.GetBatch(ctx, b.ID, 1, 50)
	require.NoError(t, err)
	require.Equal(t, "failed", batch.Items[0].Status)
	require.Equal(t, "timeout", batch.Items[0].FailureCode)
	require.Empty(t, batch.Items[0].Answers)
}

func TestCandyRepositoryKeepsResultsUntilWholeBatchFinishes(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	firstID, heldID := candyIntegrationAccount(t), candyIntegrationAccount(t)
	batch := candyIntegrationBatch(t, repo, firstID, heldID)
	first, err := repo.Claim(ctx)
	require.NoError(t, err)
	first.Status = "normal"
	_, err = repo.Complete(ctx, first)
	require.NoError(t, err)
	held, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, heldID, held.AccountID)
	for range 6 {
		candyIntegrationBatch(t, repo, firstID)
		item, claimErr := repo.Claim(ctx)
		require.NoError(t, claimErr)
		item.Status = "normal"
		_, err = repo.Complete(ctx, item)
		require.NoError(t, err)
	}
	active, err := repo.GetBatch(ctx, batch.ID, 1, 50)
	require.NoError(t, err)
	require.Len(t, active.Items, 2, "an unfinished batch keeps its completed items")
	require.Nil(t, active.FinishedAt)
	held.Status = "normal"
	_, err = repo.Complete(ctx, held)
	require.NoError(t, err)
	finished, err := repo.GetBatch(ctx, batch.ID, 1, 50)
	require.NoError(t, err)
	require.Equal(t, 2, finished.Total)
	require.Equal(t, 2, finished.Counts["normal"], "original aggregate remains frozen after pruning")
	require.Equal(t, 1, finished.RetainedTotal)
}
