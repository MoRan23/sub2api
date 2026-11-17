//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type billingLanePrivateContextKey struct{}

type snapshotLaneRepo struct {
	*laneTestRepo
	muSnapshots     sync.Mutex
	snapshots       map[string]*TotalQuotaSpendSnapshot
	retainedContext bool
	atomicCalls     int
	atomicLog       *UsageLog
}

func (r *snapshotLaneRepo) capture(ctx context.Context, cmds ...*UsageBillingCommand) {
	r.muSnapshots.Lock()
	defer r.muSnapshots.Unlock()
	if ctx.Value(billingLanePrivateContextKey{}) != nil {
		r.retainedContext = true
	}
	for _, cmd := range cmds {
		r.snapshots[cmd.RequestID] = cmd.TotalQuotaSpendSnapshot
	}
}

func (r *snapshotLaneRepo) Apply(ctx context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.capture(ctx, cmd)
	return r.laneTestRepo.Apply(ctx, cmd)
}

func (r *snapshotLaneRepo) ApplyBatch(ctx context.Context, cmds []*UsageBillingCommand) ([]UsageBillingBatchOutcome, error) {
	r.capture(ctx, cmds...)
	return r.laneTestRepo.ApplyBatch(ctx, cmds)
}

func (r *snapshotLaneRepo) ApplyWithUsageLog(ctx context.Context, cmd *UsageBillingCommand, log *UsageLog) (*UsageBillingApplyResult, error) {
	r.capture(ctx, cmd)
	r.atomicCalls++
	r.atomicLog = log
	return &UsageBillingApplyResult{Applied: true}, nil
}

func TestUsageBillingLane_PerCommandSnapshotAndContextOwnership(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "batch"
		if fallback {
			name = "single_fallback"
		}
		t.Run(name, func(t *testing.T) {
			base := newLaneTestRepo()
			if fallback {
				base.batchErr = errors.New("force fallback")
			}
			repo := &snapshotLaneRepo{laneTestRepo: base, snapshots: make(map[string]*TotalQuotaSpendSnapshot)}
			lane := newUsageBillingLaneCombiner(repo, 8)
			defer lane.Stop()
			out := make(chan laneTestResult, 3)
			release := base.block("first")
			applyAsync(lane, laneTestCommand(7, "first", 1), out)
			waitStarted(t, base, "first")
			for i, requestID := range []string{"old", "new"} {
				snapshot := &TotalQuotaSpendSnapshot{SubscriptionID: 9, EventIDs: []int64{int64(i + 1)}, OverflowEventID: int64(i + 1)}
				ctx := WithTotalQuotaSpendSnapshot(context.WithValue(context.Background(), billingLanePrivateContextKey{}, make([]byte, 1024)), snapshot)
				cmd := laneTestCommand(7, requestID, 1)
				go func() {
					result, err := lane.Apply(ctx, cmd)
					out <- laneTestResult{requestID: requestID, res: result, err: err}
				}()
				waitPending(t, lane, 7, i+1)
				// The queued command must own the small snapshot, not its caller.
				snapshot.EventIDs[0] = 999
				snapshot.OverflowEventID = 999
			}
			close(release)
			for id, result := range collectResults(t, out, 3) {
				require.NoError(t, result.err, id)
			}
			repo.muSnapshots.Lock()
			defer repo.muSnapshots.Unlock()
			require.False(t, repo.retainedContext)
			require.Equal(t, []int64{1}, repo.snapshots["old"].EventIDs)
			require.Equal(t, []int64{2}, repo.snapshots["new"].EventIDs)
			require.EqualValues(t, 1, repo.snapshots["old"].OverflowEventID)
			require.EqualValues(t, 2, repo.snapshots["new"].OverflowEventID)
		})
	}
}

func TestUsageBillingLane_AtomicUsageDelegatesWithoutOrdinaryBatching(t *testing.T) {
	repo := &snapshotLaneRepo{laneTestRepo: newLaneTestRepo(), snapshots: make(map[string]*TotalQuotaSpendSnapshot)}
	lane := NewUsageBillingLaneCombiner(repo)
	defer lane.Stop()
	snapshot := &TotalQuotaSpendSnapshot{SubscriptionID: 9, EventIDs: []int64{1}}
	ctx := WithTotalQuotaSpendSnapshot(context.Background(), snapshot)
	cmd := laneTestCommand(7, "grok-safety:physical-call", 1)
	log := &UsageLog{RequestID: cmd.RequestID}
	result, err := lane.ApplyWithUsageLog(ctx, cmd, log)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, 1, repo.atomicCalls)
	require.Same(t, log, repo.atomicLog)
	require.Empty(t, repo.applyCalls)
	require.Empty(t, repo.batchCalls)
	snapshot.EventIDs[0] = 99
	require.Equal(t, []int64{1}, repo.snapshots[cmd.RequestID].EventIDs)

	plain := NewUsageBillingLaneCombiner(newLaneTestRepo())
	defer plain.Stop()
	_, err = plain.ApplyWithUsageLog(ctx, cmd, log)
	require.ErrorIs(t, err, ErrAtomicUsageBillingUnavailable)
}

func TestUsageBillingCommand_SnapshotDoesNotChangeDedupFingerprint(t *testing.T) {
	cmd := laneTestCommand(7, "same", 1)
	cmd.Normalize()
	fingerprint := cmd.RequestFingerprint
	ctx := WithTotalQuotaSpendSnapshot(context.Background(), &TotalQuotaSpendSnapshot{SubscriptionID: 9, EventIDs: []int64{1, 2}})
	owned := snapshotUsageBillingCommand(ctx, cmd)
	owned.Normalize()
	require.Equal(t, fingerprint, owned.RequestFingerprint)
	require.Nil(t, cmd.TotalQuotaSpendSnapshot)
}

func TestUsageRecordSnapshot_PreservesUpstreamPriceMultiplier(t *testing.T) {
	original := &ForwardResult{UpstreamPriceMultiplier: 1.1}
	frozen := SnapshotRecordUsageInput(&RecordUsageInput{Result: original})
	original.UpstreamPriceMultiplier = 2
	require.Equal(t, 1.1, frozen.Result.UpstreamPriceMultiplier)
}
