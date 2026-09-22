//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexMultiProxyPostgresConfiguration(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		mode := "single"
		if bulk {
			mode = "bulk"
		}
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			oldIDs := service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(f.account))
			original := f.state
			original.CollectorProxyID, original.LastCollectorProxyID, original.CollectorExtendedCount = oldIDs[0], oldIDs[0], 2
			original.CollectorAttemptID = uuid.NewString()
			ok, err := f.states.SaveCAS(ctx, original, original.Version)
			require.NoError(t, err)
			require.True(t, ok)
			config := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: []int64{f.proxyID, oldIDs[0]}}
			updated := f.change(t, ctx, bulk, config, nil)
			storedConfig := service.CodexTurnStateConfigForAccount(updated)
			require.Equal(t, config.CollectorProxyIDs, storedConfig.CollectorProxyIDs)
			require.Equal(t, &f.proxyID, storedConfig.CollectorProxyID)
			require.NotContains(t, updated.Extra[service.CodexTurnStateExtraKey], "collector_proxy_id")
			key := f.key
			key.Generation = service.CodexTurnStateGenerationForAccount(updated)
			stored, err := f.states.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, f.proxyID, stored.CollectorProxyID)
			require.Zero(t, stored.CollectorExtendedCount)
			require.Empty(t, stored.CollectorAttemptID)
			require.Equal(t, original.LastCollectorProxyID, stored.LastCollectorProxyID)
			require.Equal(t, original.ExpiresAt, stored.ExpiresAt)
			require.Equal(t, original.EncryptedToken, stored.EncryptedToken)
			legacy := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}
			if bulk {
				_, err = f.admin.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{f.key.OwnerAccountID}, CodexTurnState: &legacy})
			} else {
				_, err = f.admin.UpdateAccount(ctx, f.key.OwnerAccountID, &service.UpdateAccountInput{CodexTurnState: &legacy})
			}
			require.ErrorContains(t, err, "collector_proxy_ids")
			unchanged, err := f.accounts.GetByID(ctx, f.key.OwnerAccountID)
			require.NoError(t, err)
			unchanged, err = service.ResolveOpenAIOAuthCredentialAccount(ctx, f.accounts, unchanged, f.key.OSFamily)
			require.NoError(t, err)
			require.Equal(t, key.Generation, service.CodexTurnStateGenerationForAccount(unchanged))
			config.CollectorProxyIDs = []int64{oldIDs[0], f.proxyID}
			reordered := f.change(t, ctx, bulk, config, nil)
			require.NotEqual(t, key.Generation, service.CodexTurnStateGenerationForAccount(reordered))
			key.Generation = service.CodexTurnStateGenerationForAccount(reordered)
			stored, err = f.states.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, oldIDs[0], stored.CollectorProxyID)
			config.CollectorProxyIDs = []int64{}
			config.CollectorProxyID = &f.proxyID
			cleared := f.change(t, ctx, bulk, config, nil)
			require.Empty(t, service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(cleared)))
			key.Generation = service.CodexTurnStateGenerationForAccount(cleared)
			stored, err = f.states.Get(ctx, key)
			require.NoError(t, err)
			require.Zero(t, stored.CollectorProxyID)
			require.Equal(t, original.ExpiresAt, stored.ExpiresAt)
		})
	}
}

func TestCodexMultiProxyPostgresLegacyFormatPreservesGeneration(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		mode := "single"
		if bulk {
			mode = "bulk"
		}
		t.Run(mode, func(t *testing.T) {
			f := newCodexProxyChangeFixture(t)
			ids := service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(f.account))
			updated := f.change(t, context.Background(), bulk, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: ids}, nil)
			require.Equal(t, f.key.Generation, service.CodexTurnStateGenerationForAccount(updated))
			stored, err := f.states.Get(context.Background(), f.key)
			require.NoError(t, err)
			require.Equal(t, f.state.Version, stored.Version)
		})
	}
}

func TestCodexMultiProxyPostgresReferences(t *testing.T) {
	ctx := context.Background()
	f := newCodexProxyChangeFixture(t)
	oldIDs := service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(f.account))
	f.change(t, ctx, false, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: []int64{f.proxyID, oldIDs[0]}}, nil)
	proxies := NewProxyRepository(integrationEntClient, integrationDB).(*proxyRepository)
	for _, id := range []int64{f.proxyID, oldIDs[0]} {
		count, err := proxies.CountAccountsByProxyID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		require.ErrorIs(t, proxies.Delete(ctx, id), service.ErrProxyInUse)
		summaries, err := proxies.ListAccountSummariesByProxyID(ctx, id)
		require.NoError(t, err)
		require.Len(t, summaries, 1)
	}
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET proxy_id=$2 WHERE id=$1`, f.key.OwnerAccountID, f.proxyID)
	require.NoError(t, err)
	counts, err := proxies.GetAccountCountsForProxies(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts[f.proxyID], "business and collector references count once")
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET proxy_id=NULL, extra=jsonb_set(extra, '{codex_turn_state}', jsonb_build_object('collector_proxy_ids','[]'::jsonb,'collector_proxy_id',$2::bigint)) WHERE id=$1`, f.key.OwnerAccountID, f.proxyID)
	require.NoError(t, err)
	count, err := proxies.CountAccountsByProxyID(ctx, f.proxyID)
	require.NoError(t, err)
	require.Zero(t, count, "explicit empty list overrides legacy reference")
	counts, err = proxies.GetAccountCountsForProxies(ctx)
	require.NoError(t, err)
	require.Zero(t, counts[f.proxyID])
}

func TestCodexMultiProxyPostgresMissingProxyAndDeletionLock(t *testing.T) {
	ctx := context.Background()
	f := newCodexProxyChangeFixture(t)
	ids := service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(f.account))
	invalid := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: []int64{f.proxyID, 9223372036854775807}}
	_, err := f.admin.UpdateAccount(ctx, f.key.OwnerAccountID, &service.UpdateAccountInput{CodexTurnState: &invalid})
	require.ErrorIs(t, err, service.ErrProxyNotFound)
	stored, err := f.accounts.GetByID(ctx, f.key.OwnerAccountID)
	require.NoError(t, err)
	stored, err = service.ResolveOpenAIOAuthCredentialAccount(ctx, f.accounts, stored, f.key.OSFamily)
	require.NoError(t, err)
	require.Equal(t, f.key.Generation, service.CodexTurnStateGenerationForAccount(stored))
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	config := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: []int64{ids[0], f.proxyID}}
	_, err = f.admin.UpdateAccount(dbent.NewTxContext(ctx, tx), f.key.OwnerAccountID, &service.UpdateAccountInput{CodexTurnState: &config})
	require.NoError(t, err)
	done := make(chan error, 1)
	deleteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() { done <- NewProxyRepository(integrationEntClient, integrationDB).Delete(deleteCtx, f.proxyID) }()
	select {
	case err := <-done:
		t.Fatalf("second proxy deletion bypassed uncommitted list lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	require.ErrorIs(t, <-done, service.ErrProxyInUse)
}
