package service

import (
	"context"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Options is strictly local: opening a benchmark dialog never probes a provider.
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
	out := &CandyTestOptions{Models: []CandyTestModelOption{}, Accounts: []CandyTestAccountOptions{}}
	union := make(map[string]CandyTestModelOption)
	for _, id := range ids {
		entry := CandyTestAccountOptions{AccountID: id, Models: []CandyTestModelOption{}}
		account := byID[id]
		if account == nil {
			entry.SkipReason = "account_missing"
		} else {
			entry.AccountName = account.Name
			if account.Platform != PlatformOpenAI {
				entry.SkipReason = "unsupported_platform"
			} else {
				entry.Models = candyTestAccountModelOptions(account)
				if len(entry.Models) == 0 {
					entry.SkipReason = "no_supported_models"
				}
			}
		}
		out.Accounts = append(out.Accounts, entry)
		for _, model := range entry.Models {
			if prior, exists := union[model.ID]; exists {
				for _, effort := range model.ReasoningEfforts {
					if !containsCandyEffort(prior.ReasoningEfforts, effort) {
						prior.ReasoningEfforts = append(prior.ReasoningEfforts, effort)
					}
				}
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

func candyTestAccountModelOptions(account *Account) []CandyTestModelOption {
	models := AppendOpenAIConfiguredTestModels(append([]openai.Model(nil), openai.DefaultModels...), account)
	if snapshot := account.GetUpstreamModelMetadataSnapshot(); snapshot != nil {
		for id := range snapshot.Models {
			models = append(models, openai.Model{ID: id, DisplayName: id})
		}
	}
	out := []CandyTestModelOption{}
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || seen[id] || strings.Contains(id, "*") || !account.IsModelSupported(id) {
			continue
		}
		seen[id] = true
		actual := id
		if !account.IsOpenAIPassthroughEnabled() {
			actual = account.GetMappedModel(id)
		}
		if !candyModelCanAnswer(actual) {
			continue
		}
		option := CandyTestModelOption{ID: id, DisplayName: model.DisplayName, ReasoningEfforts: []string{}}
		if option.DisplayName == "" {
			option.DisplayName = id
		}
		metadata, saved := account.GetUpstreamModelMetadata(actual)
		if saved {
			if metadata.Reasoning == nil || *metadata.Reasoning {
				option.ReasoningEfforts = normalizeCandyEfforts(metadata.SupportedReasoningLevels)
			}
		} else if bundledCodexModelDefault(actual) != nil {
			descriptor := newConfiguredCodexModelDescriptor(actual)
			for _, level := range descriptor.SupportedReasoningLevels {
				option.ReasoningEfforts = append(option.ReasoningEfforts, level.Effort)
			}
			option.ReasoningEfforts = normalizeCandyEfforts(option.ReasoningEfforts)
		}
		out = append(out, option)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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
