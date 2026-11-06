package service

import (
	"context"
	mathrand "math/rand"
	"sort"
	"time"
)

func newCandidateWithLoad(candidate CandidateView, load *AccountLoadInfo) accountWithLoad {
	return accountWithLoad{account: candidate.Ref(), loadInfo: load, candidateLastUsed: candidate.LastUsedAt, hasCandidateView: true}
}

func (a openAIAccountCandidateScore) lastUsedAt() *time.Time {
	if a.hasCandidateView {
		return a.candidateLastUsed
	}
	if a.account == nil {
		return nil
	}
	return a.account.LastUsedAt
}

func partitionOpenAIChatGPTSubscriptionCandidates(accounts CandidateSet) (CandidateSet, CandidateSet) {
	subscriptions := make(CandidateSet, 0, len(accounts))
	regular := make(CandidateSet, 0, len(accounts))
	for _, candidate := range accounts {
		if candidate.Ref() != nil && (candidate.IsOpenAIChatGPTSubscription() || candidate.IsCodexEngine()) {
			subscriptions = append(subscriptions, candidate)
		} else {
			regular = append(regular, candidate)
		}
	}
	return subscriptions, regular
}

func buildOpenAICandidateLoadRequest(accounts CandidateSet) []AccountWithConcurrency {
	requests := make([]AccountWithConcurrency, 0, len(accounts))
	for _, candidate := range accounts {
		if candidate.Ref() != nil {
			requests = append(requests, AccountWithConcurrency{ID: candidate.ID, MaxConcurrency: candidate.EffectiveLoadFactor()})
		}
	}
	return requests
}

func candidateAccountRefs(candidates CandidateSet) []*Account {
	refs := make([]*Account, len(candidates))
	for i := range candidates {
		refs[i] = candidates[i].Ref()
	}
	return refs
}

func candidateViewPriorityLRULess(a, b CandidateView) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.LastUsedAt == nil {
		return b.LastUsedAt != nil
	}
	return b.LastUsedAt != nil && a.LastUsedAt.Before(*b.LastUsedAt)
}

func prioritizeOpenAICompactCandidates(candidates CandidateSet) CandidateSet {
	ordered := make(CandidateSet, 0, len(candidates))
	for _, tier := range []int{2, 1, 0} {
		for _, candidate := range candidates {
			if openAICompactSupportTier(candidate.Ref()) == tier {
				ordered = append(ordered, candidate)
			}
		}
	}
	return ordered
}

func (a accountWithLoad) lastUsedAt() *time.Time {
	if a.hasCandidateView {
		return a.candidateLastUsed
	}
	if a.account == nil {
		return nil
	}
	return a.account.LastUsedAt
}

func (s *GatewayService) filterCandidatesBySchedulingThreshold(ctx context.Context, candidates CandidateSet) CandidateSet {
	if s == nil || s.rateLimitService == nil {
		return candidates
	}
	out := candidates[:0]
	for i := range candidates {
		if !s.rateLimitService.ApplyCandidateSchedulingThreshold(ctx, &candidates[i]) {
			out = append(out, candidates[i])
		}
	}
	return out
}

func (s *OpenAIGatewayService) filterOpenAICandidatesBySchedulingThreshold(ctx context.Context, candidates CandidateSet) CandidateSet {
	if s == nil || s.rateLimitService == nil {
		return candidates
	}
	out := candidates[:0]
	for i := range candidates {
		if !s.rateLimitService.ApplyCandidateSchedulingThreshold(ctx, &candidates[i]) {
			out = append(out, candidates[i])
		}
	}
	return out
}

func sortCandidateViewsByPriorityAndLastUsed(accounts CandidateSet, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		switch {
		case a.LastUsedAt == nil && b.LastUsedAt != nil:
			return true
		case a.LastUsedAt != nil && b.LastUsedAt == nil:
			return false
		case a.LastUsedAt == nil && b.LastUsedAt == nil:
			return preferOAuth && a.Type != b.Type && a.Type == AccountTypeOAuth
		default:
			return a.LastUsedAt.Before(*b.LastUsedAt)
		}
	})
	for i := 0; i < len(accounts); {
		j := i + 1
		for j < len(accounts) && accounts[i].Priority == accounts[j].Priority && sameLastUsedAt(accounts[i].LastUsedAt, accounts[j].LastUsedAt) {
			j++
		}
		shuffleCandidateViews(accounts[i:j], preferOAuth)
		i = j
	}
}

func sortCandidateViewsForFallback(accounts CandidateSet, preferOAuth bool, mode string) {
	if mode != "random" {
		sortCandidateViewsByPriorityAndLastUsed(accounts, preferOAuth)
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority < accounts[j].Priority
		}
		return preferOAuth && accounts[i].Type != accounts[j].Type && accounts[i].Type == AccountTypeOAuth
	})
	for i := 0; i < len(accounts); {
		j := i + 1
		for j < len(accounts) && accounts[i].Priority == accounts[j].Priority {
			j++
		}
		// Match the legacy random mode, which randomizes the entire priority tier.
		shuffleCandidateViews(accounts[i:j], false)
		i = j
	}
}

func shuffleCandidateViews(accounts CandidateSet, preferOAuth bool) {
	if len(accounts) < 2 {
		return
	}
	if !preferOAuth {
		mathrand.Shuffle(len(accounts), func(i, j int) { accounts[i], accounts[j] = accounts[j], accounts[i] })
		return
	}
	// Stable partition only request-local views; account snapshots remain untouched.
	partitioned := make(CandidateSet, 0, len(accounts))
	for _, account := range accounts {
		if account.Type == AccountTypeOAuth {
			partitioned = append(partitioned, account)
		}
	}
	pivot := len(partitioned)
	for _, account := range accounts {
		if account.Type != AccountTypeOAuth {
			partitioned = append(partitioned, account)
		}
	}
	copy(accounts, partitioned)
	mathrand.Shuffle(pivot, func(i, j int) { accounts[i], accounts[j] = accounts[j], accounts[i] })
	mathrand.Shuffle(len(accounts)-pivot, func(i, j int) { accounts[pivot+i], accounts[pivot+j] = accounts[pivot+j], accounts[pivot+i] })
}
