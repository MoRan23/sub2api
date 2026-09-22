package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexStateWorkerLifecycleAccounts struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *codexStateWorkerLifecycleAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	return codexStateTestScopeAccount(r.accounts[id]), nil
}

func (r *codexStateWorkerLifecycleAccounts) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	accounts := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account := r.accounts[id]; account != nil {
			accounts = append(accounts, codexStateTestScopeAccount(account))
		}
	}
	return accounts, nil
}

func (r *codexStateWorkerLifecycleAccounts) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	return codexStateTestSlot(r.accounts[id], os)
}

func (r *codexStateWorkerLifecycleAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, "windows")
	if err != nil {
		return nil, err
	}
	return []*OpenAIOAuthOSCredential{slot}, nil
}

func TestCodexTurnStateWorkersBoundConcurrencyAndStopCancelsCollectors(t *testing.T) {
	isolateCodexHistory(t)
	now := time.Now().UTC()
	accounts := &codexStateWorkerLifecycleAccounts{accounts: make(map[int64]*Account, 17)}
	repo := newCodexStateMemoryRepo()
	for id := int64(1); id <= 17; id++ {
		accounts.accounts[id] = codexStateTestScopeAccount(&Account{
			ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"access_token": fmt.Sprintf("synthetic-worker-token-%d", id), "plan_type": "plus"},
			Extra: map[string]any{
				CodexTurnStateExtraKey:           map[string]any{"enabled": true, "account_type": "personal", "collector_proxy_id": float64(2)},
				CodexTurnStateGenerationExtraKey: "gen1",
			},
		})
		key := CodexTurnStateKey{OwnerAccountID: id, OSFamily: "windows", Model: "gpt-5", Generation: "gen1"}
		repo.records[key] = CodexTurnStateRecord{
			OwnerAccountID: id, OSFamily: key.OSFamily, Model: key.Model, Generation: key.Generation, Version: 1,
			LastBusinessAt: now, DemandAt: now, DemandReason: "extended_shape", CollectionStatus: "pending",
		}
	}
	type cancellation struct {
		ownerID int64
		err     error
	}
	started := make(chan int64, 17)
	canceled := make(chan cancellation, 17)
	collector := codexStateTestCollector(func(ctx context.Context, request CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		started <- request.Account.ID
		<-ctx.Done()
		canceled <- cancellation{ownerID: request.Account.ID, err: ctx.Err()}
		return CodexTurnStateCollectResult{}, ctx.Err()
	})
	s := NewCodexTurnStateService(repo, accounts, codexStateTestEncryptor{}, collector)
	s.modelPolicy = newCodexStateTestModelPolicy("gpt-5")
	ctx, cancel := context.WithCancel(context.Background())
	var stopOnce sync.Once
	stopped := make(chan struct{})
	stop := func() {
		stopOnce.Do(func() {
			go func() {
				s.Stop()
				close(stopped)
			}()
		})
	}
	t.Cleanup(func() {
		cancel()
		stop()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("worker lifecycle cleanup did not stop the service")
		}
	})
	s.Start(ctx)
	owners := make(map[int64]bool, 16)
	startDeadline := time.NewTimer(5 * time.Second)
	defer startDeadline.Stop()
	for range 16 {
		select {
		case id := <-started:
			require.NotContains(t, owners, id, "each blocked collector must belong to a distinct credential owner")
			owners[id] = true
		case <-startDeadline.C:
			t.Fatalf("only %d of 16 collectors started", len(owners))
		}
	}
	select {
	case id := <-started:
		t.Fatalf("owner %d started while all 16 collector workers were blocked", id)
	case <-time.After(150 * time.Millisecond):
	}

	// Stop must cancel its own worker contexts while the parent remains live.
	stop()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not promptly cancel and join blocked collectors")
	}
	require.NoError(t, ctx.Err())
	canceledOwners := make(map[int64]bool, 16)
	for range 16 {
		select {
		case result := <-canceled:
			require.ErrorIs(t, result.err, context.Canceled)
			require.NotContains(t, canceledOwners, result.ownerID)
			canceledOwners[result.ownerID] = true
		default:
			t.Fatal("Stop returned before every running collector observed cancellation")
		}
	}
	require.Equal(t, owners, canceledOwners)
	select {
	case id := <-started:
		t.Fatalf("queued owner %d started a collector during shutdown", id)
	default:
	}
}
