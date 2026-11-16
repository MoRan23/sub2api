package service

import (
	"context"
	"time"
)

// ShadowParentHealth contains no credential or account relationship graph.
type ShadowParentHealth struct {
	ID                     int64
	Platform               string
	Type                   string
	Status                 string
	ExpiresAt              *time.Time
	AutoPauseOnExpired     bool
	TempUnschedulableUntil *time.Time
}

func (p ShadowParentHealth) Account() *Account {
	return &Account{ID: p.ID, Platform: p.Platform, Type: p.Type, Status: p.Status,
		ExpiresAt: p.ExpiresAt, AutoPauseOnExpired: p.AutoPauseOnExpired, TempUnschedulableUntil: p.TempUnschedulableUntil}
}

type ShadowParentHealthReader interface {
	GetShadowParentHealthBatch(context.Context, []int64) (map[int64]ShadowParentHealth, error)
}

type shadowParentHealthContextKey struct{}

func (s *defaultOpenAIAccountScheduler) withShadowParentHealth(ctx context.Context, accounts CandidateSet) context.Context {
	seen := make(map[int64]bool)
	var ids []int64
	for _, candidate := range accounts {
		if candidate.Account != nil && candidate.IsShadow() && !seen[*candidate.ParentAccountID] {
			id := *candidate.ParentAccountID
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || s == nil || s.service == nil {
		return ctx
	}
	parents := make(map[int64]*Account, len(ids))
	var projected map[int64]ShadowParentHealth
	if snapshot := s.service.schedulerSnapshot; snapshot != nil {
		if reader, ok := snapshot.cache.(ShadowParentHealthReader); ok {
			projected, _ = reader.GetShadowParentHealthBatch(ctx, ids)
		}
	}
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if p, ok := projected[id]; ok {
			parents[id] = p.Account()
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		if reader, ok := s.service.accountRepo.(ShadowParentHealthReader); ok {
			values, err := reader.GetShadowParentHealthBatch(ctx, missing)
			if err == nil {
				for _, id := range missing {
					if p, ok := values[id]; ok {
						parents[id] = p.Account()
					} else {
						parents[id] = nil
					}
				}
			} else {
				return ctx
			}
		} else {
			for _, id := range missing {
				parents[id] = s.lookupShadowParentAccount(ctx, id)
			}
		}
	}
	return context.WithValue(ctx, shadowParentHealthContextKey{}, parents)
}
