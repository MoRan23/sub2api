package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

func (s *DashboardService) GetGroupUsageSummaryByIDs(ctx context.Context, today time.Time, ids []int64) ([]usagestats.GroupUsageSummary, error) {
	if len(ids) == 0 {
		return []usagestats.GroupUsageSummary{}, nil
	}
	if repo, ok := s.usageRepo.(interface {
		GetGroupUsageSummaryByIDs(context.Context, time.Time, []int64) ([]usagestats.GroupUsageSummary, error)
	}); ok {
		return repo.GetGroupUsageSummaryByIDs(ctx, today, ids)
	}
	return nil, fmt.Errorf("scoped group usage summary unavailable")
}
