//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicLongContextUsesSharedCatalogProjection(t *testing.T) {
	account := Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"claude-alias": "claude-upstream"}}}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
		"claude-upstream": {ContextWindow: 1_000_000},
	}})
	repo := &catalogProjectionRepoStub{accounts: []Account{account}}
	svc := &GatewayService{accountRepo: repo}
	group := &Group{ID: 7, Platform: PlatformAnthropic}
	models := []string{"claude-alias"}
	first := svc.ExpandAnthropicLongContextModels(context.Background(), group, models)
	require.Equal(t, []string{"claude-alias", "claude-alias[1m]"}, first)
	second := svc.ExpandAnthropicLongContextModels(context.Background(), group, models)
	require.Same(t, &first[0], &second[0])
	require.Equal(t, 1, repo.calls)
	require.Zero(t, repo.fullCalls)
	require.Equal(t, []string{"claude-alias"}, models)
	group.ModelRoutingEnabled = true
	group.ModelRouting = map[string][]int64{"claude-alias": {2}}
	require.Equal(t, models, svc.ExpandAnthropicLongContextModels(context.Background(), group, models))
	group.ModelRoutingEnabled = false
	repo.accounts[0].SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
		"claude-upstream": {ContextWindow: 899_999},
	}})
	svc.catalogStore().Invalidate()
	require.Equal(t, models, svc.ExpandAnthropicLongContextModels(context.Background(), group, models))
	require.Equal(t, 2, repo.calls)
}
