package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

func (s *GatewayService) ExpandAnthropicLongContextModels(ctx context.Context, group *Group, models []string) []string {
	var groupID int64
	if group != nil {
		groupID = group.ID
	}
	accounts, _, err := s.loadCatalogAccounts(ctx, groupID)
	if err != nil {
		return models
	}
	if len(accounts) == 0 || accounts[0].catalogProjectionVersion == "" {
		return expandAnthropicLongContextModels(group, models, accounts)
	}
	key := struct {
		GroupID        int64
		Version        string
		Visible        []int64
		Models         []string
		RoutingEnabled bool
		Routing        map[string][]int64
	}{GroupID: groupID, Version: accounts[0].catalogProjectionVersion, Models: models}
	for i := range accounts {
		key.Visible = append(key.Visible, accounts[i].ID)
	}
	if group != nil {
		key.RoutingEnabled, key.Routing = group.ModelRoutingEnabled, group.ModelRouting
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return models
	}
	value, err := s.catalogStore().Load(ctx, "anthropic-long-context:"+codexModelsManifestBodyETag(encoded), func(context.Context) (any, int64, error) {
		expanded := expandAnthropicLongContextModels(group, models, accounts)
		var size int64
		for _, model := range expanded {
			size += int64(len(model)) + 16
		}
		return expanded, size, nil
	})
	if err != nil {
		return models
	}
	return value.([]string)
}

func expandAnthropicLongContextModels(group *Group, models []string, accounts []Account) []string {
	result := cloneStringSlice(models)
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		seen[strings.ToLower(model)] = true
	}
	for _, model := range models {
		if strings.HasSuffix(strings.ToLower(model), claudeCodeLongContextModelSuffix) || strings.Contains(model, "*") {
			continue
		}
		var routedAccountIDs []int64
		if group != nil {
			routedAccountIDs = group.GetRoutingAccountIDs(model)
		}
		for i := range accounts {
			account := &accounts[i]
			if !account.IsAnthropic() || !account.IsModelSupported(model) ||
				(len(routedAccountIDs) > 0 && !slices.Contains(routedAccountIDs, account.ID)) {
				continue
			}
			metadata, ok := account.GetUpstreamModelMetadata(account.GetMappedModel(model))
			variant := model + claudeCodeLongContextModelSuffix
			if ok && metadata.ContextWindow >= 900_000 && !seen[strings.ToLower(variant)] {
				result = append(result, variant)
				seen[strings.ToLower(variant)] = true
				break
			}
		}
	}
	return result
}
