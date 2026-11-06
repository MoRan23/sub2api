package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// CandidateView holds an immutable scheduling projection and request-local hot
// state. Ref is read-only; acquire a private copy with MutableAccount before any
// mutation. Sorting/filtering copies these small views, never an Account slab.
type CandidateView struct {
	*Account
	LastUsedAt *time.Time
	owned      bool
}

type CandidateSet []CandidateView

func NewCandidateView(account *Account, lastUsedAt *time.Time) CandidateView {
	if lastUsedAt == nil && account != nil {
		lastUsedAt = account.LastUsedAt
	}
	return CandidateView{Account: account, LastUsedAt: lastUsedAt}
}

func (v CandidateView) Ref() *Account { return v.Account }

// MutableAccount allocates only for the candidate actually being modified.
// Nested maps remain read-only until their existing copy-on-write helpers run.
func (v *CandidateView) MutableAccount() *Account {
	if v == nil || v.Account == nil {
		return nil
	}
	if !v.owned {
		copy := *v.Account
		copy.LastUsedAt = v.LastUsedAt
		v.Account = &copy
		v.owned = true
	}
	return v.Account
}

func CandidatesFromAccounts(accounts []Account) CandidateSet {
	views := make(CandidateSet, len(accounts))
	for i := range accounts {
		accounts[i].PrepareSharedSnapshot()
		views[i] = NewCandidateView(&accounts[i], accounts[i].LastUsedAt)
	}
	return views
}

func CandidatesFromAccountPointers(accounts []*Account) CandidateSet {
	views := make(CandidateSet, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			views = append(views, NewCandidateView(account, account.LastUsedAt))
		}
	}
	return views
}

// SchedulerCandidateCache is optional to preserve existing cache adapters.
type SchedulerCandidateCache interface {
	GetCandidateSnapshot(context.Context, SchedulerBucket) (CandidateSet, bool, error)
}

func (s *SchedulerSnapshotService) ListSchedulableCandidates(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) (CandidateSet, bool, error) {
	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	bucket := s.bucketFor(groupID, platform, s.resolveMode(platform, hasForcePlatform))
	if err := ctx.Err(); err != nil {
		return nil, useMixed, err
	}
	var token SchedulerBucketWriteToken
	canPublish := false
	if s.cache != nil {
		var views CandidateSet
		var hit bool
		var err error
		if candidateCache, ok := s.cache.(SchedulerCandidateCache); ok {
			views, hit, err = candidateCache.GetCandidateSnapshot(ctx, bucket)
		} else {
			var accounts []*Account
			accounts, hit, err = s.cache.GetSnapshot(ctx, bucket)
			if hit && err == nil {
				views = CandidatesFromAccountPointers(accounts)
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, useMixed, ctxErr
		}
		if err == nil && hit {
			return views, useMixed, nil
		}
		if err != nil {
			slog.Warn("scheduler candidate cache read failed", "bucket", bucket.String(), "error", err)
		}
		token, err = s.cache.CaptureBucketWriteToken(ctx, bucket)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, useMixed, ctxErr
		}
		canPublish = err == nil
		if err != nil && !errors.Is(err, ErrSchedulerBucketRetired) && !errors.Is(err, ErrSchedulerBucketWriteFenced) {
			slog.Warn("scheduler candidate publish token failed", "bucket", bucket.String(), "error", err)
		}
	}
	if err := s.guardFallback(ctx); err != nil {
		return nil, useMixed, err
	}
	fallbackCtx, cancel := s.withFallbackTimeout(ctx)
	defer cancel()
	accounts, err := s.loadAccountsFromDB(fallbackCtx, bucket, useMixed)
	if err != nil {
		return nil, useMixed, err
	}
	if err := ctx.Err(); err != nil {
		return nil, useMixed, err
	}
	if s.cache != nil && canPublish {
		if err := s.cache.SetSnapshot(fallbackCtx, bucket, token, accounts); err != nil {
			slog.Debug("scheduler candidate snapshot publish failed", "bucket", bucket.String(), "error", err)
		}
	}
	return CandidatesFromAccounts(accounts), useMixed, nil
}

// ApplyCandidateSchedulingThreshold preserves threshold side effects while
// allocating an Account copy only when this evaluation can modify it.
func (s *RateLimitService) ApplyCandidateSchedulingThreshold(ctx context.Context, candidate *CandidateView) bool {
	if candidate == nil {
		return false
	}
	account := candidate.Ref()
	if s == nil || s.settingService == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || !account.IsActive() || !account.Schedulable {
		return false
	}
	now := time.Now().UTC()
	thresholds := s.settingService.GetAccountSchedulingThresholds(ctx)
	decision := EvaluateAccountSchedulingThreshold(account, thresholds, now)
	needsCopy := decision.ShouldPause && decision.Until != nil && decision.Until.After(now)
	if !needsCopy {
		fable := evaluateAnthropicFableSchedulingThreshold(account, thresholds, now)
		// Clone before entering the time-sensitive active-limit check: an old
		// limit can expire between these two evaluations.
		needsCopy = fable.ShouldPause && fable.Until != nil && fable.Until.After(now)
	}
	if needsCopy {
		account = candidate.MutableAccount()
	}
	return s.applyAccountSchedulingThreshold(ctx, account, thresholds, now)
}
