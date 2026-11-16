package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type compositeOwnershipIndex map[string]CompositeModelOwnership

func buildCompositeOwnershipIndex(accounts []Account) (compositeOwnershipIndex, int64) {
	index := make(compositeOwnershipIndex)
	var size int64
	for _, account := range accounts {
		if hasEmptyEngineModelCatalog(&account) {
			continue
		}
		for model, target := range stringMappingFromRaw(account.Credentials["model_mapping"]) {
			if strings.TrimSpace(target) == "" {
				continue
			}
			value, exists := index[model]
			if !exists {
				value.TargetPlatform = account.Platform
				value.Matched = true
				size += int64(len(model)+len(account.Platform)) + 96
			} else if value.TargetPlatform != account.Platform {
				value.TargetPlatform = ""
				value.Matched = false
				value.Ambiguous = true
			}
			value.OpenAIGatewayOAuthClaimed = value.OpenAIGatewayOAuthClaimed || (account.IsOAuth() && domain.UsesOpenAIGateway(account.Platform))
			index[model] = value
		}
	}
	return index, size
}
func (s *GatewayService) compositeOwnershipIndex(ctx context.Context, groupID int64) (compositeOwnershipIndex, error) {
	store := s.catalogStore()
	value, err := store.Load(ctx, fmt.Sprintf("ownership:%d", groupID), func(ctx context.Context) (any, int64, error) {
		var accounts []Account
		projection, err := loadCatalogProjection(ctx, s.accountRepo, store, groupID)
		if err != nil {
			return nil, 0, err
		}
		if projection != nil {
			accounts = projection.accounts
		} else {
			accounts, err = s.accountRepo.ListModelAvailabilityCandidates(ctx, &groupID, domain.ConcretePlatformIDs(), false)
		}
		if err != nil {
			return nil, 0, err
		}
		index, size := buildCompositeOwnershipIndex(accounts)
		return index, size, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(compositeOwnershipIndex), nil
}
