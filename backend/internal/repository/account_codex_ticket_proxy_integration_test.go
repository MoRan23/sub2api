//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketProxyPostgresSwitchPreservesExistingBundle(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			config := service.CodexTurnStateConfigForAccount(f.account)
			for _, requested := range []*bool{new(false), nil, new(true), nil} {
				previous := service.CodexTurnStateConfigForAccount(f.account)
				config.UseTicketProxy = requested
				updated := f.change(t, ctx, bulk, config, nil)
				want := service.CodexTurnStateUseTicketProxy(previous)
				if requested != nil {
					want = *requested
				}
				require.Equal(t, want, service.CodexTurnStateUseTicketProxy(service.CodexTurnStateConfigForAccount(updated)))
				require.Equal(t, f.key.Generation, service.CodexTurnStateGenerationForAccount(updated))
				stored, err := f.states.Get(ctx, f.key)
				require.NoError(t, err)
				require.Equal(t, f.state.Version, stored.Version)
				require.Equal(t, f.state.EncryptedToken, stored.EncryptedToken)
				require.Equal(t, f.state.BundleBinding, stored.BundleBinding)
				f.account = updated
			}
			config.Enabled = false
			updated := f.change(t, ctx, bulk, config, nil)
			require.NotEqual(t, f.key.Generation, service.CodexTurnStateGenerationForAccount(updated), "cache enablement must still fence old bundles")
		})
	}
}

func TestCodexTicketProxyPostgresLegacyBulkPreservesEachAccount(t *testing.T) {
	ctx := context.Background()
	first, second := newCodexProxyChangeFixture(t), newCodexProxyChangeFixture(t)
	config := service.CodexTurnStateConfigForAccount(first.account)
	config.UseTicketProxy = new(false)
	first.change(t, ctx, false, config, nil)
	legacy := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: []int64{}}
	_, err := first.admin.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{
		AccountIDs: []int64{first.account.ID, second.account.ID}, CodexTurnState: &legacy,
	})
	require.NoError(t, err)
	for id, want := range map[int64]bool{first.account.ID: false, second.account.ID: true} {
		account, err := first.accounts.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, service.CodexTurnStateUseTicketProxy(service.CodexTurnStateConfigForAccount(account)))
	}
}
