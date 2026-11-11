package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const openAIGroupOfficialCatalogMaxSources = 4

type officialCatalogDeadlineContextKey struct{}

// BuildOpenAIGroupModelsCatalog separates public routing names from ModelInfo
// metadata. Official descriptions never grant account access: all names are
// projected through the group's existing routes before the final allowlist.
// Composite and other platforms deliberately retain their existing builders.
func (s *OpenAIGatewayService) BuildOpenAIGroupModelsCatalog(ctx context.Context, group *Group, clientVersion, ifNoneMatch string, codex bool, maxAccountSwitches ...int) (*OpenAIModelsResponse, *Account, error) {
	if s == nil || s.accountRepo == nil || group == nil || group.Platform != PlatformOpenAI {
		return nil, nil, fmt.Errorf("OpenAI group model catalog requires an OpenAI group and account repository")
	}
	visible, accounts, err := loadCodexGroupCatalogAccounts(ctx, s.accountRepo, group.ID)
	if err != nil {
		return nil, nil, err
	}
	var fixed []pinnedOpenAIModelsResult
	var selected *Account
	if group.CodexModelsManifestConfig.Enabled {
		fetch := func(fetchCtx context.Context, account *Account) (*OpenAIModelsResponse, error) {
			// The existing Codex source accepts both models[] and data[]. Preserve
			// that raw membership before its compatibility metadata is rebuilt.
			response, fetchErr := s.fetchCodexModelsManifest(fetchCtx, account, clientVersion, "", true)
			if fetchErr != nil {
				return nil, fetchErr
			}
			raw := response.upstreamSourceBody
			if len(raw) == 0 {
				raw = response.Body
			}
			if _, _, manifestErr := modelCatalogEntries(raw, "models"); manifestErr == nil {
				response.Body, fetchErr = standardOpenAIModelsBody(raw, true)
			} else {
				response.Body, fetchErr = standardOpenAIModelsBody(raw, false)
			}
			if fetchErr != nil {
				return nil, fetchErr
			}
			response.Body, fetchErr = projectAccountModelsBody(response.Body, account, group, false)
			return response, fetchErr
		}
		fixed, err = s.fetchPinnedOpenAIModels(ctx, group, fetch)
		if err != nil && ctx.Err() == nil && group.CodexModelsManifestConfig.FallbackToScheduler {
			maxSwitches := 3
			if len(maxAccountSwitches) != 0 && maxAccountSwitches[0] > 0 {
				maxSwitches = maxAccountSwitches[0]
			}
			fixed, err = s.fetchScheduledOpenAIModels(ctx, group, maxSwitches, fetch)
		}
		if err != nil {
			return nil, nil, err
		}
		selected = fixed[0].account
		accounts = make([]Account, 0, len(fixed))
		for _, result := range fixed {
			accounts = append(accounts, *result.account)
		}
	}

	// A public official file supplies metadata only. Its data never enters the
	// OAuth authorization capability observer, including when the live source
	// is unavailable or a whitelist adds a model absent from that authorization.
	official, officialAccount := s.fetchGroupOfficialCodexModels(ctx, group, visible, clientVersion)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	public, err := s.getPublicOfficialCodexModels(ctx)
	if err != nil {
		return nil, nil, err
	}
	if selected == nil {
		selected = officialAccount
	}
	if official == nil {
		official = public
	}
	return buildOpenAIGroupModelsCatalog(group, visible, accounts, fixed, official, public, ifNoneMatch, codex, selected)
}

// fetchGroupOfficialCodexModels tries only distinct, currently authorized OAuth
// sources from this group. An API-key-only group does no per-account probing.
// Every attempt and detached cache refresh shares the same overall deadline.
func (s *OpenAIGatewayService) fetchGroupOfficialCodexModels(ctx context.Context, group *Group, accounts []Account, clientVersion string) (*OpenAIModelsResponse, *Account) {
	ctx, cancel := context.WithTimeout(ctx, codexModelsManifestRequestTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	ctx = context.WithValue(ctx, officialCatalogDeadlineContextKey{}, deadline)
	candidates := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account.IsOpenAIOAuth() && account.IsSchedulable() {
			candidates = append(candidates, account)
		}
	}
	// Pinned OAuth sources retain the administrator's ordering; other sources
	// follow the same priority/LRU policy used by ordinary catalog discovery.
	pinnedOrder := make(map[int64]int)
	if group.CodexModelsManifestConfig.Enabled {
		for i, id := range group.CodexModelsManifestConfig.AccountIDs {
			pinnedOrder[id] = i + 1
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := pinnedOrder[candidates[i].ID], pinnedOrder[candidates[j].ID]
		if left != right && (left != 0 || right != 0) {
			return left != 0 && (right == 0 || left < right)
		}
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		if candidates[i].LastUsedAt == nil || candidates[j].LastUsedAt == nil {
			return candidates[i].LastUsedAt == nil && candidates[j].LastUsedAt != nil
		}
		return candidates[i].LastUsedAt.Before(*candidates[j].LastUsedAt)
	})
	seenOwners := make(map[int64]bool)
	attempts := 0
	for i := range candidates {
		if ctx.Err() != nil || attempts == openAIGroupOfficialCatalogMaxSources {
			break
		}
		account := &candidates[i]
		ownerID := account.ID
		if account.IsShadow() {
			ownerID = *account.ParentAccountID
		}
		// All candidates in this request use the same captured OS. Deduplicating
		// the owner before credential resolution also bounds shared-parent reads.
		if seenOwners[ownerID] {
			continue
		}
		seenOwners[ownerID] = true
		attempts++
		scoped, err := ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account, OpenAIRequestOSFromContext(ctx).Family)
		if err != nil {
			continue
		}
		manifest, err := s.fetchCodexModelsManifest(ctx, scoped, clientVersion, "", true)
		if err == nil {
			return manifest, scoped
		}
	}
	return nil, nil
}

func officialCatalogRefreshDeadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Value(officialCatalogDeadlineContextKey{}).(time.Time)
	return deadline
}

func buildOpenAIGroupModelsCatalog(group *Group, visible, accounts []Account, fixed []pinnedOpenAIModelsResult, official, public *OpenAIModelsResponse, ifNoneMatch string, codex bool, selected *Account) (*OpenAIModelsResponse, *Account, error) {
	envelope, officialEntries, err := modelCatalogEntries(official.Body, "models")
	if err != nil {
		return nil, nil, err
	}
	_, publicEntries, err := modelCatalogEntries(public.Body, "models")
	if err != nil {
		return nil, nil, err
	}
	officialByID := indexCodexCatalogModels(officialEntries)
	publicByID := indexCodexCatalogModels(publicEntries)
	ids := make([]string, 0, len(officialEntries))
	standardEntries := make(map[string]json.RawMessage)
	if fixed != nil {
		for _, result := range fixed {
			_, entries, decodeErr := modelCatalogEntries(result.response.Body, "data")
			if decodeErr != nil {
				return nil, nil, decodeErr
			}
			for _, raw := range entries {
				var entry struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &entry) != nil || strings.TrimSpace(entry.ID) == "" {
					continue
				}
				if _, exists := standardEntries[entry.ID]; !exists {
					ids = append(ids, entry.ID)
					standardEntries[entry.ID] = raw
				}
			}
		}
	} else {
		for _, raw := range officialEntries {
			var entry struct {
				Slug string `json:"slug"`
			}
			if json.Unmarshal(raw, &entry) == nil {
				ids = append(ids, entry.Slug)
			}
		}
		mappingAccounts := make([]Account, 0, len(visible))
		for _, account := range visible {
			if !openAIGroupCatalogIgnoresModelMapping(&account) {
				mappingAccounts = append(mappingAccounts, account)
			}
		}
		ids = append(ids, openAIConfiguredCodexModelIDs(mappingAccounts)...)
		// Preserve standard Images discovery without adding stale LLM defaults
		// back into the official catalog. Codex pickers continue to omit media.
		for _, id := range openai.DefaultModelIDs() {
			if isCodexDedicatedMediaModel(id) {
				ids = append(ids, id)
			}
		}
		if group.ModelAllowlistEnabled() {
			for _, id := range group.ModelAllowlist.Models {
				if !strings.Contains(id, "*") {
					ids = append(ids, id)
				}
			}
		}
		filtered := ids[:0]
		for _, id := range ids {
			if openAIGroupCatalogCanRoute(group, visible, id) {
				filtered = append(filtered, id)
			}
		}
		ids = filtered
	}
	ids = uniqueOpenAIGroupCatalogIDs(ids)
	if group.ModelAllowlistEnabled() {
		ids = group.ModelAllowlist.FilterForListing(ids)
	}
	ids = filterRetiredOpenAIModelIDs(accounts, ids)
	// Automatic internal models have the same explicit opt-in rule in both
	// representations. Media remain visible only in the standard API catalog.
	filteredIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if !codex && isCodexDedicatedMediaModel(id) {
			filteredIDs = append(filteredIDs, id)
		} else if len(FilterCodexModelIDsForGroup([]string{id}, group)) != 0 {
			filteredIDs = append(filteredIDs, id)
		}
	}
	ids = filteredIDs

	models := make([]json.RawMessage, 0, len(ids))
	unknownIDs := make([]string, 0)
	descriptors := make(map[string]json.RawMessage, len(ids))
	for _, id := range ids {
		target := openAIGroupCatalogMetadataTarget(group, accounts, id)
		raw, overridden := codexUserModelDescriptorOverride(canonicalCyberDescriptorID(target))
		if !overridden {
			raw = lookupOfficialCodexCatalogModel(officialByID, target)
			if raw == nil {
				raw = lookupOfficialCodexCatalogModel(publicByID, target)
			}
		}
		if raw == nil {
			unknownIDs = append(unknownIDs, id)
			continue
		}
		descriptors[id], err = projectOfficialCodexDescriptor(raw, id)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(unknownIDs) > 0 {
		fallback, buildErr := buildCodexModelsManifestForAccounts(PlatformOpenAI, unknownIDs, openAIGroupCatalogSynthesisAccounts(accounts), group, nil, true)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		_, entries, decodeErr := modelCatalogEntries(fallback, "models")
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		for id, raw := range indexCodexCatalogModels(entries) {
			descriptors[id], err = projectOfficialCodexDescriptor(raw, id)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	for _, id := range ids {
		if raw := descriptors[id]; raw != nil {
			models = append(models, raw)
		} else if !codex && isCodexDedicatedMediaModel(id) {
			raw, _ := json.Marshal(map[string]string{"slug": id, "display_name": id})
			models = append(models, raw)
		}
	}
	envelope["models"], err = json.Marshal(models)
	if err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}
	if !codex {
		body, err = standardOpenAIModelsBody(body, true)
		if err != nil {
			return nil, nil, err
		}
		// Standard fixed-source catalogs keep provider extension fields. Their
		// names and order are the same as the final manifest's public projection.
		if len(standardEntries) > 0 {
			body, err = preserveFixedStandardModelEntries(body, standardEntries, descriptors)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	response := &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body)}
	return openAIModelsResponseForClient(response, ifNoneMatch), selected, nil
}

func indexCodexCatalogModels(entries []json.RawMessage) map[string]json.RawMessage {
	indexed := make(map[string]json.RawMessage, len(entries))
	for _, raw := range entries {
		var entry struct {
			Slug string `json:"slug"`
		}
		if json.Unmarshal(raw, &entry) == nil && strings.TrimSpace(entry.Slug) != "" {
			if _, exists := indexed[entry.Slug]; !exists {
				indexed[entry.Slug] = raw
			}
		}
	}
	return indexed
}

func lookupOfficialCodexCatalogModel(index map[string]json.RawMessage, modelID string) json.RawMessage {
	if raw := index[modelID]; raw != nil {
		return raw
	}
	canonical := canonicalizeOpenAIModelAliasSpelling(modelID)
	if raw := index[canonical]; raw != nil {
		return raw
	}
	// Only established suffixes inherit a descriptor; unknown names never use
	// the broad forwarding normalizer's heuristic family fallback.
	var match string
	for id := range index {
		if len(id) > len(match) && openai.IsKnownCodexModelVariant(canonical, id) {
			match = id
		}
	}
	return index[match]
}

func canonicalCyberDescriptorID(modelID string) string {
	canonical := canonicalizeOpenAIModelAliasSpelling(modelID)
	for _, family := range []string{"gpt-5.6-cyber", "gpt-6-cyber"} {
		if openai.IsKnownCodexModelVariant(canonical, family) {
			return family
		}
	}
	return canonical
}

func projectOfficialCodexDescriptor(raw json.RawMessage, publicID string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var sourceID string
	_ = json.Unmarshal(fields["slug"], &sourceID)
	fields["slug"], _ = json.Marshal(publicID)
	fields["shell_type"] = json.RawMessage(`"unified_exec"`)
	if sourceID != publicID {
		fields["display_name"], _ = json.Marshal(publicID)
	}
	return json.Marshal(fields)
}

func uniqueOpenAIGroupCatalogIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !strings.Contains(id, "*") && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func openAIGroupCatalogCanRoute(group *Group, accounts []Account, modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || strings.Contains(modelID, "*") {
		return false
	}
	routed := group.GetRoutingAccountIDs(modelID)
	for i := range accounts {
		account := &accounts[i]
		if account.IsOpenAI() && (len(routed) == 0 || containsInt64(routed, account.ID)) && account.IsModelSupported(modelID) {
			return true
		}
	}
	return false
}

func openAIGroupCatalogMetadataTarget(group *Group, accounts []Account, modelID string) string {
	routed := group.GetRoutingAccountIDs(modelID)
	targets := make(map[string]struct{})
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != PlatformOpenAI || !account.IsModelSupported(modelID) || (len(routed) != 0 && !containsInt64(routed, account.ID)) {
			continue
		}
		target := modelID
		if !openAIGroupCatalogIgnoresModelMapping(account) {
			if mapped, matched := account.ResolveMappedModel(modelID); matched && strings.TrimSpace(mapped) != "" {
				target = strings.TrimSpace(mapped)
			}
		}
		targets[target] = struct{}{}
	}
	if len(targets) == 1 {
		for target := range targets {
			return target
		}
	}
	return modelID
}

func openAIGroupCatalogIgnoresModelMapping(account *Account) bool {
	return account.IsOpenAIPassthroughEnabled() && !account.IsCodexEngine() && !shouldForwardOpenAIResponsesViaRawChatCompletions(account)
}

func openAIGroupCatalogSynthesisAccounts(accounts []Account) []Account {
	// Legacy synthesis consults mappings in several capability helpers. Supply
	// independent projections so stale passthrough mappings cannot hide unknown
	// models or borrow unrelated model capabilities, without changing Composite.
	projected := append([]Account(nil), accounts...)
	for i := range projected {
		if openAIGroupCatalogIgnoresModelMapping(&projected[i]) {
			projected[i].Credentials = maps.Clone(projected[i].Credentials)
			delete(projected[i].Credentials, "model_mapping")
		}
	}
	return projected
}

func preserveFixedStandardModelEntries(body []byte, source, descriptors map[string]json.RawMessage) ([]byte, error) {
	envelope, entries, err := modelCatalogEntries(body, "data")
	if err != nil {
		return nil, err
	}
	for i, raw := range entries {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &entry) == nil && source[entry.ID] != nil {
			var fields, descriptor, projected map[string]json.RawMessage
			if err := json.Unmarshal(source[entry.ID], &fields); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &projected); err != nil {
				return nil, err
			}
			if len(descriptors[entry.ID]) > 0 {
				if err := json.Unmarshal(descriptors[entry.ID], &descriptor); err != nil {
					return nil, err
				}
				// Provider-specific standard fields survive, but fields shared with
				// the official ModelInfo cannot override the selected description.
				for key := range fields {
					if key == "id" || key == "object" || key == "created" || key == "owned_by" {
						continue
					}
					if value, exists := descriptor[key]; exists {
						fields[key] = value
					}
				}
			}
			fields["id"] = projected["id"]
			fields["object"] = projected["object"]
			if displayName := projected["display_name"]; displayName != nil {
				fields["display_name"] = displayName
			}
			entries[i], err = json.Marshal(fields)
			if err != nil {
				return nil, err
			}
		}
	}
	envelope["data"], err = json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}
