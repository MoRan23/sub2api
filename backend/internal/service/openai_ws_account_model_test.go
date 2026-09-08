package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIWSModelRestrictionRepo struct {
	AccountRepository
	account *Account
	err     error
}

func (r *openAIWSModelRestrictionRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, r.err
}

func TestOpenAIWSAccountModelRestrictionLatestOAuth(t *testing.T) {
	selected := &Account{
		ID: 6799, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"gpt-5.6-sol": "gpt-5.6-sol", "gpt-5.6-terra": "gpt-5.6-terra",
		}},
	}
	latest := *selected
	latest.Credentials = map[string]any{"model_mapping": map[string]any{
		"gpt-5.6-terra": "gpt-5.6-terra", "public-alias": "gpt-5.6-terra", "allowed-*": "gpt-5.6-terra",
	}}
	repo := &openAIWSModelRestrictionRepo{account: &latest}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := context.Background()

	require.True(t, selected.IsModelSupported("gpt-5.6-sol"), "the connection snapshot still allows the removed model")
	require.False(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "gpt-5.6-sol"))
	for _, model := range []string{"gpt-5.6-terra", "public-alias", "allowed-model"} {
		require.True(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, model), model)
	}
	require.False(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "unknown-alias"))
	require.False(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "gpt-5.6-sol-high"))

	latest.Credentials = nil
	require.True(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "gpt-5.6-sol"), "no explicit restriction keeps the default model behavior")
	latest.Credentials = map[string]any{"model_mapping": map[string]any{"gpt-5.6-terra": "gpt-5.6-terra"}}
	latest.Extra = map[string]any{"openai_passthrough": true}
	require.True(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "gpt-5.6-sol"), "explicit passthrough retains its documented unrestricted semantics")

	repo.err = errors.New("database unavailable")
	require.False(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "gpt-5.6-sol"), "lookup failures must not reuse stale allowlists")
	repo.err = nil
	repo.account = nil
	require.False(t, svc.SupportsOpenAIWSModelLatest(ctx, selected, "gpt-5.6-sol"))
	var nilService *OpenAIGatewayService
	require.False(t, nilService.SupportsOpenAIWSModelLatest(ctx, nil, "gpt-5.6-sol"))
}
