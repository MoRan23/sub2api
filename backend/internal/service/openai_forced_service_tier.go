package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const openAIServiceTierCatalogWait = 2 * time.Second

// This immutable index is built from the upstream document before any local
// model metadata is completed. Cache hits share it without copying the catalog.
type openAIServiceTierCatalog struct {
	ultrafast map[string]bool
}

func parseOpenAIServiceTierCatalog(body []byte) *openAIServiceTierCatalog {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	field, idField := "models", "slug"
	if _, exists := envelope[field]; !exists {
		field, idField = "data", "id"
	}
	_, entries, err := modelCatalogEntries(body, field)
	if err != nil {
		return nil
	}
	catalog := &openAIServiceTierCatalog{ultrafast: make(map[string]bool)}
	for _, raw := range entries {
		var entry map[string]json.RawMessage
		var id string
		if json.Unmarshal(raw, &entry) != nil || json.Unmarshal(entry[idField], &id) != nil {
			return nil
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		supported := false
		var tiers []map[string]json.RawMessage
		if rawTiers, present := entry["service_tiers"]; present && json.Unmarshal(rawTiers, &tiers) != nil {
			return nil
		}
		for _, tier := range tiers {
			var tierID string
			if json.Unmarshal(tier["id"], &tierID) != nil {
				continue
			}
			if tierID == OpenAIFastTierUltrafast {
				supported = true
				break
			}
		}
		// Conflicting duplicate entries do not grant a capability.
		if prior, exists := catalog.ultrafast[id]; exists {
			supported = supported && prior
		}
		catalog.ultrafast[id] = supported
	}
	return catalog
}

func (s *OpenAIGatewayService) openAIUltrafastCapability(ctx context.Context, account *Account, model string) string {
	if s == nil || account == nil || strings.TrimSpace(model) == "" {
		return "unavailable"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, openAIServiceTierCatalogWait)
	defer cancel()
	manifest, err := s.fetchCodexModelsManifestForPurpose(ctx, account, "", "", true, true)
	if err != nil || manifest == nil || manifest.serviceTierCatalog == nil || manifest.catalogDegraded ||
		manifest.observedAt.IsZero() || !time.Now().Before(manifest.observedAt.Add(openAIModelsCacheTTL)) {
		return "unavailable"
	}
	if manifest.serviceTierCatalog.ultrafast[strings.TrimSpace(model)] {
		return "supported"
	}
	return "unsupported"
}

func openAIModelsResponseForRequest(manifest *OpenAIModelsResponse, ifNoneMatch string, capabilityOnly bool) *OpenAIModelsResponse {
	if !capabilityOnly {
		return openAIModelsResponseForClient(manifest, ifNoneMatch)
	}
	if manifest == nil {
		return nil
	}
	return &OpenAIModelsResponse{serviceTierCatalog: manifest.serviceTierCatalog, observedAt: manifest.observedAt, catalogDegraded: manifest.catalogDegraded}
}

// Account policy owns the choice even when its Ultrafast capability check fails:
// preserve the client tier instead of falling through to the group's Fast rule.
func (s *OpenAIGatewayService) resolveOpenAIForcedServiceTier(ctx context.Context, account *Account, model, requested string) string {
	if account == nil || account.Platform != PlatformOpenAI {
		return ""
	}
	requested = normalizedOpenAIServiceTierValue(requested)
	if openAIGuardianPolicyKind(ctx, account) != "" {
		recordOpenAIServiceTierPolicy(ctx, account, "none", requested, "", "guardian")
		return ""
	}
	source, forced := "", ""
	switch {
	case account.IsForceOpenAIUltrafastEnabled():
		source, forced = "account", OpenAIFastTierUltrafast
	case account.IsForceOpenAIFastEnabled():
		source, forced = "account", OpenAIFastTierPriority
	default:
		if ctx != nil {
			group, _ := ctx.Value(ctxkey.Group).(*Group)
			if IsGroupContextValid(group) && groupSupportsOpenAIFast(group.Platform) {
				switch {
				case group.ForceOpenAIUltrafast:
					source, forced = "group", OpenAIFastTierUltrafast
				case group.ForceOpenAIFast:
					source, forced = "group", OpenAIFastTierPriority
				}
			}
		}
	}
	if forced == "" {
		recordOpenAIServiceTierPolicy(ctx, account, "none", requested, "", "not_required")
		return ""
	}
	capability := "not_required"
	if forced == OpenAIFastTierUltrafast {
		capability = s.openAIUltrafastCapability(ctx, account, model)
	}
	recordOpenAIServiceTierPolicy(ctx, account, source, requested, forced, capability)
	if forced == OpenAIFastTierUltrafast && capability != "supported" {
		return ""
	}
	return forced
}
