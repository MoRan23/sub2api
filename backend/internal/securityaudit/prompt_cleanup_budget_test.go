package securityaudit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type boundedCleanupStore struct {
	enforcementNotificationStore
	calls     int
	cutoff    time.Time
	remaining int64
}

func (s *boundedCleanupStore) PruneEnforcementHistory(ctx context.Context, now time.Time, limit int) (int64, error) {
	s.calls++
	if s.cutoff.IsZero() {
		s.cutoff = now
	} else if !s.cutoff.Equal(now) {
		panic("cutoff moved")
	}
	if _, ok := ctx.Deadline(); !ok {
		panic("missing cleanup budget")
	}
	n := min(s.remaining, int64(limit))
	s.remaining -= n
	return n, nil
}
func TestPromptCleanupBoundedBacklog(t *testing.T) {
	for _, tc := range []struct {
		rows  int64
		calls int
	}{{2100, 3}, {100000, 20}, {0, 1}} {
		store := &boundedCleanupStore{remaining: tc.rows}
		worker := &PromptNotificationWorker{repo: store}
		worker.pruneHistory(context.Background())
		require.Equal(t, tc.calls, store.calls)
	}
}
