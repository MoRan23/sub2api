package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type AttributionProbeAccountModels struct {
	AccountID   int64    `json:"account_id"`
	AccountName string   `json:"account_name"`
	Models      []string `json:"models"`
	Status      string   `json:"status"`
	Error       string   `json:"error,omitempty"`
}

type AttributionProbeModels struct {
	Models   []string                        `json:"models"`
	Accounts []AttributionProbeAccountModels `json:"accounts"`
}

// ProbeModels suggests actual upstream IDs, independently of both the detector
// reference bank and business routing. These suggestions never constrain probe
// execution: a downgraded whitelist must not prevent testing a higher model.
func (s *ModelAttributionService) ProbeModels(ctx context.Context, ids []int64) (*AttributionProbeModels, error) {
	if len(ids) == 0 || len(ids) > 500 {
		return nil, ErrAttributionInvalid
	}
	unique := make([]int64, 0, len(ids))
	seenIDs := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id < 1 {
			return nil, ErrAttributionInvalid
		}
		if !seenIDs[id] {
			seenIDs[id] = true
			unique = append(unique, id)
		}
	}
	accounts, err := s.accounts.GetByIDs(ctx, unique)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}
	out := &AttributionProbeModels{Models: []string{}, Accounts: make([]AttributionProbeAccountModels, len(unique))}
	work := make(chan int, len(unique))
	for i, id := range unique {
		entry := AttributionProbeAccountModels{AccountID: id, Models: []string{}, Status: "error"}
		account := byID[id]
		if reason := AttributionSkipReason(account, time.Now(), true); reason != "" {
			entry.Error = reason
		} else {
			entry.AccountName = account.Name
			entry.Models = attributionProbeModelIDs(configuredUpstreamModelsForCapabilitySync(account))
			entry.Error = "model_catalog_failed"
			if len(entry.Models) > 0 {
				entry.Status = "partial"
			}
			work <- i
		}
		out.Accounts[i] = entry
	}
	close(work)
	// Bound the entire picker request as well as individual upstream calls. Even
	// a large cold batch returns its local candidates instead of waiting for
	// hundreds of sequential directory timeouts.
	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for range min(3, len(unique)) {
		workers.Go(func() {
			for i := range work {
				if fetchCtx.Err() != nil || s.fetchProbeModels == nil {
					return
				}
				entry := &out.Accounts[i]
				response, fetchErr := s.fetchProbeModels(fetchCtx, byID[entry.AccountID])
				if fetchErr != nil || response == nil || response.NotModified {
					continue
				}
				models, _, parseErr := extractUpstreamModelCatalog(response.Body, false)
				if parseErr != nil {
					continue
				}
				entry.Models = attributionProbeModelIDs(append(entry.Models, models...))
				entry.Status, entry.Error = "success", ""
			}
		})
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, entry := range out.Accounts {
		out.Models = append(out.Models, entry.Models...)
	}
	out.Models = attributionProbeModelIDs(out.Models)
	return out, nil
}

func attributionProbeModelIDs(models []string) []string {
	result := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 200 || strings.ContainsAny(model, "*\r\n\t ") || seen[model] {
			continue
		}
		seen[model] = true
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}
