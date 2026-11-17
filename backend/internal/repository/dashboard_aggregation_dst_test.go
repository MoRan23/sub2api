//go:build unit

package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestDashboardAggregationDayBoundsDST(t *testing.T) {
	originalTZ := timezone.Location().String()
	t.Cleanup(func() { require.NoError(t, timezone.Init(originalTZ)) })
	for _, tc := range []struct {
		name, zone, start, end, wantStart, wantEnd string
		wantHours                                  int
	}{
		{"spring forward", "America/New_York", "2026-03-08T12:00:00-04:00", "2026-03-08T18:00:00-04:00", "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00", 23},
		{"fall back", "America/New_York", "2026-11-01T12:00:00-05:00", "2026-11-01T18:00:00-05:00", "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00", 25},
		{"missing midnight", "America/Sao_Paulo", "2018-11-04T12:00:00-02:00", "2018-11-04T18:00:00-02:00", "2018-11-04T01:00:00-02:00", "2018-11-05T00:00:00-02:00", 23},
		{"exact end excludes next day", "America/New_York", "2026-11-01T12:00:00-05:00", "2026-11-02T00:00:00-05:00", "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00", 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, timezone.Init(tc.zone))
			parse := func(value string) time.Time {
				v, err := time.Parse(time.RFC3339, value)
				require.NoError(t, err)
				return v
			}
			start, end, err := dashboardAggregationDayBounds(parse(tc.start), parse(tc.end))
			require.NoError(t, err)
			require.True(t, start.Equal(parse(tc.wantStart)), "start: %s", start)
			require.True(t, end.Equal(parse(tc.wantEnd)), "end: %s", end)
			require.Equal(t, time.Duration(tc.wantHours)*time.Hour, end.Sub(start))
			require.Equal(t, tc.wantStart[:10], start.Format("2006-01-02"), "SQL date arguments must retain the local date")
			require.Equal(t, tc.wantEnd[:10], end.Format("2006-01-02"))
		})
	}
}
