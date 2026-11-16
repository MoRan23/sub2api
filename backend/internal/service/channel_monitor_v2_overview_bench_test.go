package service

import (
	"context"
	"testing"
	"time"
)

// Measures aggregate reuse and per-viewer projection; database latency is excluded.
func BenchmarkChannelMonitorV2OverviewCache(b *testing.B) {
	for _, cached := range []bool{false, true} {
		name := "cold"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			repo := overviewTestRepository()
			groups := make([]Group, 100)
			now := time.Now().UTC()
			for i := range groups {
				id := int64(i + 1)
				groups[i] = Group{ID: id, Status: StatusActive, Platform: PlatformOpenAI}
				repo.matrix.Items = append(repo.matrix.Items, ChannelMonitorV2MatrixRow{GroupID: &id, Buckets: make([]ChannelMonitorV2TrendPoint, 18)})
			}
			svc := NewChannelMonitorV2Service(repo)
			svc.now = func() time.Time { return now }
			filter, _ := svc.ParseFilter("90m", nil, nil, nil)
			ctx := context.Background()
			if cached {
				if _, err := svc.GetGroupOverviewWithOptions(ctx, filter, groups, nil, false, false); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					svc.overviewCache = channelOverviewCache{}
				}
				if _, err := svc.GetGroupOverviewWithOptions(ctx, filter, groups, nil, false, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
