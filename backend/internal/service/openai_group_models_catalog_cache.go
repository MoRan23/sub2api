package service

import (
	"context"
	"encoding/json"
	"fmt"
)

type catalogSourceReadOnlyContextKey struct{}

type catalogSourceVersion struct{ Identity, Content, ETag string }

func catalogSourceVersionOf(response *OpenAIModelsResponse) catalogSourceVersion {
	if response == nil {
		return catalogSourceVersion{}
	}
	content := response.sourceContentVersion
	if content == "" && len(response.upstreamSourceBody) > 0 {
		content = codexModelsManifestBodyETag(response.upstreamSourceBody)
	}
	if content == "" && response.ETag == "" {
		content = codexModelsManifestBodyETag(response.Body)
	}
	return catalogSourceVersion{response.sourceIdentity, content, response.ETag}
}

// Fixed-source membership is also a projection: cache it before the final
// representation lookup so a conditional request does not decode every source.
func (s *OpenAIGatewayService) cachedPinnedOpenAIGroupSource(ctx context.Context, group *Group, account *Account, source *OpenAIModelsResponse) (*OpenAIModelsResponse, error) {
	keyBody, err := json.Marshal([]any{group.ID, group.ModelAllowlist, account.ID, account.Platform, account.Type, account.GetModelMapping(), account.IsOpenAIPassthroughEnabled(), hasEmptyEngineModelCatalog(account), catalogSourceVersionOf(source)})
	if err != nil {
		return nil, err
	}
	key := "source-projection:" + codexModelsManifestBodyETag(keyBody)
	value, err := s.catalogStore().Load(ctx, key, func(context.Context) (any, int64, error) {
		response := *source
		raw := response.upstreamSourceBody
		if len(raw) == 0 {
			raw = response.Body
		}
		var err error
		if _, _, manifestErr := modelCatalogEntries(raw, "models"); manifestErr == nil {
			response.Body, err = standardOpenAIModelsBody(raw, true)
		} else {
			response.Body, err = standardOpenAIModelsBody(raw, false)
		}
		if err != nil {
			return nil, 0, err
		}
		response.Body, err = projectAccountModelsBody(response.Body, account, group, false)
		if err != nil {
			return nil, 0, err
		}
		response.ETag = codexModelsManifestBodyETag(response.Body)
		return &response, int64(len(response.Body)+len(response.upstreamSourceBody)) + 256, nil
	})
	if err != nil {
		return nil, err
	}
	response := *value.(*OpenAIModelsResponse)
	return &response, nil
}

// Sources have already passed the existing credential resolver and slot checks.
// Only the expensive group projection/serialization is shared, never admission.
func (s *OpenAIGatewayService) cachedOpenAIGroupModelsCatalog(ctx context.Context, group *Group, visible, accounts []Account, fixed []pinnedOpenAIModelsResult, official, public *OpenAIModelsResponse, ifNoneMatch, clientVersion, projectionVersion string, codex bool, selected *Account) (*OpenAIModelsResponse, *Account, error) {
	store := s.catalogStore()
	if projectionVersion == "" {
		raw, err := json.Marshal(accounts)
		if err != nil {
			return nil, nil, err
		}
		projectionVersion = codexModelsManifestBodyETag(raw)
	}
	visibleIDs := make([]int64, len(visible))
	for i := range visible {
		visibleIDs[i] = visible[i].ID
	}
	sources := make([]catalogSourceVersion, 0, len(fixed)+2)
	sources = append(sources, catalogSourceVersionOf(official), catalogSourceVersionOf(public))
	for _, result := range fixed {
		source := catalogSourceVersionOf(result.response)
		sources = append(sources, source)
	}
	var credentialIdentity any
	if selected != nil {
		credentialIdentity = []any{selected.ID, selected.OpenAIOAuthCredentialOwnerID, selected.OpenAIOAuthCredentialOS, selected.OpenAIOAuthAuthorizationGeneration, selected.OpenAIOAuthCredentialRevision, selected.OpenAIOAuthCredentialStateGeneration, selected.OpenAIOAuthCredentialEpoch, selected.ProxyID, selected.UpdatedAt}
	}
	keyBody, err := json.Marshal([]any{credentialIdentity, group.ID, group.Platform, group.ModelAllowlist, group.ModelRoutingEnabled, group.ModelRouting, group.CodexModelsManifestConfig, projectionVersion, visibleIDs, OpenAIRequestOSFromContext(ctx).Family, clientVersion, codex, sources})
	if err != nil {
		return nil, nil, err
	}
	key := fmt.Sprintf("representation:%s", codexModelsManifestBodyETag(keyBody))
	value, err := store.Load(ctx, key, func(ctx context.Context) (any, int64, error) {
		response, _, err := buildOpenAIGroupModelsCatalog(group, visible, accounts, fixed, official, public, "", codex, selected)
		if err != nil {
			return nil, 0, err
		}
		return response, int64(len(response.Body)) + int64(len(response.ETag)) + 128, nil
	})
	if err != nil {
		return nil, nil, err
	}
	cached := value.(*OpenAIModelsResponse)
	if codexModelsManifestETagMatches(ifNoneMatch, cached.ETag) {
		return &OpenAIModelsResponse{ETag: cached.ETag, NotModified: true}, selected, nil
	}
	// A response wrapper is caller-owned. The encoded bytes are immutable and are
	// written directly by the handlers; do not clone the complete JSON for a hit.
	response := *cached
	return &response, selected, nil
}
