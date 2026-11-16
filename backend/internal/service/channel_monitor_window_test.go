package service

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

type monitorWindowRepo struct {
	ChannelMonitorRepository
	windows                    []int
	latestCalls, timelineCalls int
}

func (*monitorWindowRepo) ListEnabled(context.Context) ([]*ChannelMonitor, error) {
	out := make([]*ChannelMonitor, 100)
	for i := range out {
		out[i] = &ChannelMonitor{ID: int64(i + 1), PrimaryModel: "probe", Enabled: true}
	}
	return out, nil
}
func (r *monitorWindowRepo) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*ChannelMonitorLatest, error) {
	r.latestCalls++
	return nil, nil
}
func (r *monitorWindowRepo) ComputeAvailabilityForMonitors(_ context.Context, ids []int64, days int) (map[int64][]*ChannelMonitorAvailability, error) {
	r.windows = append(r.windows, days)
	out := make(map[int64][]*ChannelMonitorAvailability, len(ids))
	for _, id := range ids {
		out[id] = []*ChannelMonitorAvailability{{Model: "probe", AvailabilityPct: float64(days)}}
	}
	return out, nil
}
func (r *monitorWindowRepo) ListRecentHistoryForMonitors(context.Context, []int64, map[int64]string, int) (map[int64][]*ChannelMonitorHistoryEntry, error) {
	r.timelineCalls++
	return nil, nil
}

func TestChannelMonitorWindowBatchQueries(t *testing.T) {
	for _, days := range []int{7, 15, 30} {
		t.Run(strconv.Itoa(days), func(t *testing.T) {
			repo := &monitorWindowRepo{}
			s := &ChannelMonitorService{repo: repo}
			rows, err := s.ListUserViewWithWindow(context.Background(), days)
			require.NoError(t, err)
			require.Len(t, rows, 100)
			require.Equal(t, 1, repo.latestCalls)
			require.Equal(t, 1, repo.timelineCalls)
			if days == 7 {
				require.Equal(t, []int{7}, repo.windows)
			} else {
				require.Equal(t, []int{7, days}, repo.windows)
			}
			require.Equal(t, 7.0, rows[0].Availability7d)
			if days == 15 {
				require.Equal(t, 15.0, *rows[0].Availability15d)
			}
			if days == 30 {
				require.Equal(t, 30.0, *rows[0].Availability30d)
			}
		})
	}
}
