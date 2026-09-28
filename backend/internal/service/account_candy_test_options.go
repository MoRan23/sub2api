package service

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Options reads each selected account's live upstream catalog, independently of
// business model routing. A failing account cannot hide other accounts' models.
func (r *AccountCandyTestTransport) Options(ctx context.Context, ids []int64) (*CandyTestOptions, error) {
	accounts, err := r.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}
	out := &CandyTestOptions{Models: []CandyTestModelOption{}, Accounts: make([]CandyTestAccountOptions, len(ids))}
	jobs := make(chan int, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(3, len(ids)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				id := ids[i]
				entry := CandyTestAccountOptions{AccountID: id, Models: []CandyTestModelOption{}}
				account := byID[id]
				if account == nil {
					entry.SkipReason = "account_missing"
				} else {
					entry.AccountName = account.Name
					if account.Platform != PlatformOpenAI {
						entry.SkipReason = "unsupported_platform"
					} else {
						models, fetchErr := r.candyTestAccountModelOptions(ctx, account)
						if fetchErr != nil {
							entry.SkipReason = "model_catalog_failed"
						} else {
							entry.Models = models
						}
						if fetchErr == nil && len(entry.Models) == 0 {
							entry.SkipReason = "no_supported_models"
						}
					}
				}
				out.Accounts[i] = entry
			}
		})
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	union := make(map[string]CandyTestModelOption)
	for _, entry := range out.Accounts {
		for _, model := range entry.Models {
			if prior, exists := union[model.ID]; exists {
				for _, effort := range model.ReasoningEfforts {
					if !containsCandyEffort(prior.ReasoningEfforts, effort) {
						prior.ReasoningEfforts = append(prior.ReasoningEfforts, effort)
					}
				}
				prior.ReasoningEfforts = normalizeCandyEfforts(prior.ReasoningEfforts)
				union[model.ID] = prior
			} else {
				union[model.ID] = model
			}
		}
	}
	for _, model := range union {
		out.Models = append(out.Models, model)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].ID < out.Models[j].ID })
	return out, nil
}

func containsCandyEffort(efforts []string, value string) bool {
	for _, effort := range efforts {
		if effort == value {
			return true
		}
	}
	return false
}

func (r *AccountCandyTestTransport) candyTestAccountModelOptions(ctx context.Context, account *Account) ([]CandyTestModelOption, error) {
	if r.fetchModels == nil {
		return nil, candyTestError("model_catalog_failed")
	}
	response, err := r.fetchModels(ctx, account)
	if err != nil || response == nil || response.NotModified {
		return nil, candyTestError("model_catalog_failed")
	}
	return candyTestUpstreamModelOptions(account, response.Body)
}

func candyTestUpstreamModelOptions(account *Account, body []byte) ([]CandyTestModelOption, error) {
	models, liveMetadata, err := extractUpstreamModelCatalog(body, false)
	if err != nil {
		return nil, candyTestError("model_catalog_failed")
	}
	out := []CandyTestModelOption{}
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model)
		if id == "" || seen[id] || strings.Contains(id, "*") {
			continue
		}
		seen[id] = true
		if !candyModelCanAnswer(id) {
			continue
		}
		metadata := liveMetadata[id]
		option := CandyTestModelOption{ID: id, DisplayName: metadata.DisplayName, ReasoningEfforts: []string{}}
		if option.DisplayName == "" {
			option.DisplayName = openaiCodexDisplayName(id)
		}
		// Saved/bundled capabilities may fill missing reasoning metadata only;
		// neither can add a model that the live upstream omitted.
		if saved, ok := account.GetUpstreamModelMetadata(id); ok {
			metadata, _ = mergeUpstreamModelMetadata(metadata, saved)
		}
		if metadata.Reasoning != nil && !*metadata.Reasoning {
			out = append(out, option)
			continue
		}
		option.ReasoningEfforts = normalizeCandyEfforts(metadata.SupportedReasoningLevels)
		if len(option.ReasoningEfforts) == 0 && bundledCodexModelDefault(id) != nil {
			descriptor := newConfiguredCodexModelDescriptor(id)
			for _, level := range descriptor.SupportedReasoningLevels {
				option.ReasoningEfforts = append(option.ReasoningEfforts, level.Effort)
			}
			option.ReasoningEfforts = normalizeCandyEfforts(option.ReasoningEfforts)
		}
		out = append(out, option)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func normalizeCandyEfforts(values []string) []string {
	result := []string{}
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
		if containsCandyEffort(values, effort) {
			result = append(result, effort)
		}
	}
	return result
}

func candyModelCanAnswer(model string) bool {
	name := strings.ToLower(model)
	for _, marker := range []string{"embedding", "moderation", "whisper", "transcribe", "tts", "image", "dall-e", "sora", "realtime", "audio"} {
		if strings.Contains(name, marker) {
			return false
		}
	}
	return true
}
