//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type codexProxyChangeFixture struct {
	key      service.CodexTurnStateKey
	state    service.CodexTurnStateRecord
	account  *service.Account
	accounts *accountRepository
	states   service.CodexTurnStateRepository
	admin    service.AdminService
	proxyID  int64
}

func newCodexProxyChangeFixture(t *testing.T) codexProxyChangeFixture {
	t.Helper()
	ctx := context.Background()
	key := createCodexStateFixture(t)
	oldProxy := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "codex-old-collector"})
	newProxy := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "codex-new-collector"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, key.OwnerAccountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, key.OwnerAccountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM proxies WHERE id IN ($1,$2)`, oldProxy.ID, newProxy.ID)
	})
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials='{"access_token":"synthetic","refresh_token":"windows-refresh","chatgpt_account_id":"workspace-1","chatgpt_user_id":"user-1","plan_type":"plus"}'::jsonb,
		extra=extra || jsonb_build_object('codex_turn_state_credential_epoch','credential-epoch',
		'codex_turn_state',jsonb_build_object('enabled',true,'account_type','personal','collector_proxy_id',$2::bigint)) WHERE id=$1`, key.OwnerAccountID, oldProxy.ID)
	require.NoError(t, err)
	accounts := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	_, err = accounts.EnsureOpenAIOAuthOSProfiles(ctx, key.OwnerAccountID)
	require.NoError(t, err)
	account, err := accounts.GetByID(ctx, key.OwnerAccountID)
	require.NoError(t, err)
	account, err = service.ResolveOpenAIOAuthCredentialAccount(ctx, accounts, account, service.OpenAIOSWindows)
	require.NoError(t, err)
	key.Generation = account.OpenAIOAuthCredentialStateGeneration
	states := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	state, err := states.BeginBusiness(ctx, key, "seed", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, states.EndBusiness(ctx, key, "seed"))
	state.EncryptedToken, state.Shape, state.Source = "synthetic-encrypted-state", "target", "collector"
	state.EncryptedCookieBundle = "synthetic-encrypted-cookie-bundle"
	state.BundleBinding = service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct"}
	state.TokenLength, state.CipherBlocks = 292, 10
	state.IssuedAt, state.ExpiresAt = now.Add(-2*time.Minute), now.Add(service.CodexTurnStateLifetime-2*time.Minute)
	state.CookieBundleExpiresAt = &state.ExpiresAt
	state.LastBusinessAt, state.LastCollectedAt = now.Add(-time.Minute), now.Add(-2*time.Minute)
	state.LastEligibleCollectionAt = state.LastBusinessAt
	state.HistoryProofObservedAt = now.Add(-3 * time.Minute)
	state.DemandReason, state.RefreshReason, state.DemandAt = "expiring", "expiring", now.Add(-time.Minute)
	state.NextCollectAt, state.LastError = now.Add(30*time.Second), "collection_failed"
	state.CollectionStatus, state.CollectionReason = "collecting", "collecting"
	state.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	ok, err := states.SaveCAS(ctx, *state, state.Version)
	require.NoError(t, err)
	require.True(t, ok)
	state, err = states.Get(ctx, key)
	require.NoError(t, err)
	state.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	admin := service.NewAdminService(nil, nil, nil, accounts, NewProxyRepository(integrationEntClient, integrationDB), nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil)
	return codexProxyChangeFixture{key: key, state: *state, account: account, accounts: accounts, states: states, admin: admin, proxyID: newProxy.ID}
}

func (f codexProxyChangeFixture) change(t *testing.T, ctx context.Context, bulk bool, config service.CodexTurnStateConfig, credentials map[string]any) *service.Account {
	t.Helper()
	if credentials != nil {
		_, err := f.accounts.BindOpenAIOAuthOSCredentials(ctx, f.key.OwnerAccountID, f.key.OSFamily, credentials, "test_reauthorization")
		require.NoError(t, err)
	}
	if bulk {
		_, err := f.admin.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{f.key.OwnerAccountID}, CodexTurnState: &config, Credentials: credentials})
		require.NoError(t, err)
	} else {
		_, err := f.admin.UpdateAccount(ctx, f.key.OwnerAccountID, &service.UpdateAccountInput{CodexTurnState: &config, Credentials: credentials})
		require.NoError(t, err)
	}
	stored, err := f.accounts.GetByID(ctx, f.key.OwnerAccountID)
	require.NoError(t, err)
	stored, err = service.ResolveOpenAIOAuthCredentialAccount(ctx, f.accounts, stored, f.key.OSFamily)
	require.NoError(t, err)
	return stored
}

func TestCodexCollectorProxyChangePostgresPreservesValidCache(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			name, _ := json.Marshal(map[string]bool{"bulk": bulk, "remove": remove})
			t.Run(string(name), func(t *testing.T) {
				ctx := context.Background()
				f := newCodexProxyChangeFixture(t)
				config := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}
				if remove {
					config.CollectorProxyID = nil
				}
				account := f.change(t, ctx, bulk, config, nil)
				key := f.key
				key.Generation = service.CodexTurnStateGenerationForAccount(account)
				require.NotEqual(t, f.key.Generation, key.Generation)
				require.Equal(t, service.CodexTurnStateCredentialEpochForAccount(f.account), service.CodexTurnStateCredentialEpochForAccount(account))
				state, err := f.states.Get(ctx, key)
				require.NoError(t, err)
				require.NotNil(t, state)
				require.Equal(t, f.state.Version+1, state.Version)
				require.Equal(t, f.state.EncryptedToken, state.EncryptedToken)
				require.Equal(t, f.state.IssuedAt, state.IssuedAt)
				require.Equal(t, f.state.ExpiresAt, state.ExpiresAt)
				require.Equal(t, f.state.Source, state.Source)
				require.Equal(t, f.state.LastBusinessAt, state.LastBusinessAt)
				require.Equal(t, f.state.LastCollectedAt, state.LastCollectedAt)
				require.Equal(t, f.state.HistoryProofObservedAt, state.HistoryProofObservedAt)
				require.Equal(t, f.state.DemandReason, state.DemandReason)
				require.Equal(t, f.state.RefreshReason, state.RefreshReason)
				require.Empty(t, state.LastError)
				require.Equal(t, f.state.NextCollectAt, state.NextCollectAt)
				require.Equal(t, "backoff", state.CollectionStatus)
				require.Equal(t, "queued", state.CollectionReason)
				ok, err := f.states.SaveCAS(ctx, f.state, f.state.Version)
				require.NoError(t, err)
				require.False(t, ok, "the old proxy cannot publish into the migrated cache")
			})
		}
	}
}

func TestCodexCollectorProxyChangePostgresPreservesSharedOSBundle(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			before := f.state
			for _, family := range []string{service.OpenAIOSLinux, service.OpenAIOSMacOS} {
				slot, err := f.accounts.GetOpenAIOAuthOSCredential(ctx, f.key.OwnerAccountID, family)
				require.NoError(t, err)
				require.NotNil(t, slot)
				key := f.key
				key.OSFamily, key.Generation = family, slot.StateGeneration
				now := time.Now().UTC()
				initial, err := f.states.BeginBusiness(ctx, key, family+"-seed", now, now.Add(time.Minute))
				require.NoError(t, err)
				require.NotNil(t, initial)
				require.NoError(t, f.states.EndBusiness(ctx, key, family+"-seed"))
				state := before
				state.OSFamily, state.Generation, state.Version = family, slot.StateGeneration, initial.Version
				state.EncryptedToken = family + "-shared-state"
				state.EncryptedCookieBundle = family + "-shared-cookie-bundle"
				ok, err := f.states.SaveCAS(ctx, state, initial.Version)
				require.NoError(t, err)
				require.True(t, ok)
				stored, err := f.states.Get(ctx, key)
				require.NoError(t, err)
				require.NotNil(t, stored)
				stored.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
				before = *stored
			}
			f.change(t, ctx, bulk, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}, nil)
			for _, family := range []string{service.OpenAIOSWindows, service.OpenAIOSLinux, service.OpenAIOSMacOS} {
				slot, err := f.accounts.GetOpenAIOAuthOSCredential(ctx, f.key.OwnerAccountID, family)
				require.NoError(t, err)
				require.NotEqual(t, before.Generation, slot.StateGeneration)
				key := before.Key()
				key.OSFamily = family
				key.Generation = slot.StateGeneration
				carried, err := f.states.Get(ctx, key)
				require.NoError(t, err)
				require.NotNil(t, carried)
				require.Equal(t, before.EncryptedToken, carried.EncryptedToken)
				require.Equal(t, before.EncryptedCookieBundle, carried.EncryptedCookieBundle)
				require.Equal(t, before.IssuedAt, carried.IssuedAt)
				require.Equal(t, before.ExpiresAt, carried.ExpiresAt)
				require.Equal(t, before.Version+1, carried.Version)
				ok, err := f.states.SaveCAS(ctx, before, before.Version)
				require.NoError(t, err)
				require.False(t, ok)
			}
		})
	}
}

func TestCodexCollectorProxyChangePostgresPreservesEarlierExpiry(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			candidate := f.state
			candidate.ExpiresAt = candidate.ExpiresAt.Add(-time.Minute)
			ok, err := f.states.SaveCAS(ctx, candidate, candidate.Version)
			require.NoError(t, err)
			require.True(t, ok)
			before, err := f.states.Get(ctx, f.key)
			require.NoError(t, err)
			require.NotNil(t, before)
			account := f.change(t, ctx, bulk, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}, nil)
			key := f.key
			key.Generation = service.CodexTurnStateGenerationForAccount(account)
			require.NotEqual(t, f.key.Generation, key.Generation)
			after, err := f.states.Get(ctx, key)
			require.NoError(t, err)
			require.NotNil(t, after)
			require.Equal(t, before.EncryptedToken, after.EncryptedToken)
			require.Equal(t, before.IssuedAt, after.IssuedAt)
			require.Equal(t, before.ExpiresAt, after.ExpiresAt, "proxy changes must neither discard nor extend an earlier local deadline")
			require.Equal(t, before.Version+1, after.Version)
			require.Equal(t, before.DemandReason, after.DemandReason)
			require.Equal(t, before.NextCollectAt, after.NextCollectAt)
			require.Equal(t, "backoff", after.CollectionStatus)
			require.Equal(t, "queued", after.CollectionReason)
		})
	}
}

func TestCodexCollectorProxyChangePostgresKeepsDemandAndAccountCooldown(t *testing.T) {
	for _, test := range []struct {
		name   string
		target bool
		paused bool
	}{
		{name: "valid cache keeps rate limit", target: true},
		{name: "abnormal demand continues"},
		{name: "auth pause reset", paused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			before := f.state
			before.LastError = "collector_rate_limited"
			if !test.target {
				before.EncryptedToken = ""
				before.EncryptedCookieBundle = ""
				before.Shape = "extended"
				before.TokenLength = 312
				before.CipherBlocks = 11
				before.DemandReason = "extended_shape"
			}
			if test.paused {
				before.CollectorPaused = true
				before.LastError = "collector_auth_rejected"
			}
			ok, err := f.states.SaveCAS(ctx, before, before.Version)
			require.NoError(t, err)
			require.True(t, ok)
			account := f.change(t, ctx, false, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}, nil)
			key := f.key
			key.Generation = service.CodexTurnStateGenerationForAccount(account)
			state, err := f.states.Get(ctx, key)
			require.NoError(t, err)
			require.NotNil(t, state)
			require.False(t, state.CollectorPaused)
			require.Equal(t, before.LastBusinessAt, state.LastBusinessAt)
			require.Equal(t, before.HistoryProofObservedAt, state.HistoryProofObservedAt)
			if test.paused {
				require.Empty(t, state.LastError)
				require.True(t, state.NextCollectAt.IsZero())
			} else {
				require.Equal(t, before.LastError, state.LastError)
				require.Equal(t, before.NextCollectAt, state.NextCollectAt)
			}
			if test.target {
				require.Equal(t, before.DemandReason, state.DemandReason)
				require.Equal(t, "queued", state.CollectionReason)
			} else {
				require.Empty(t, state.EncryptedToken)
				require.Equal(t, "extended_shape", state.DemandReason)
				require.Equal(t, "queued", state.CollectionReason)
			}
		})
	}
}

func TestCodexCollectorProxyChangePostgresDoesNotCarryOtherChanges(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		for _, change := range []string{"disable", "classification", "credentials"} {
			mode := "single"
			if bulk {
				mode = "bulk"
			}
			t.Run(mode+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				f := newCodexProxyChangeFixture(t)
				config := service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}
				var credentials map[string]any
				switch change {
				case "disable":
					config.Enabled = false
				case "classification":
					config.AccountType = "team_business"
				case "credentials":
					credentials = map[string]any{"access_token": "replacement", "plan_type": "plus"}
				}
				account := f.change(t, ctx, bulk, config, credentials)
				var generation string
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT generation FROM openai_codex_state WHERE owner_account_id=$1 AND model=$2`, f.key.OwnerAccountID, f.key.Model).Scan(&generation))
				require.Equal(t, f.key.Generation, generation)
				require.NotEqual(t, generation, service.CodexTurnStateGenerationForAccount(account))
			})
		}
	}
}

func TestCodexCollectorProxyChangePostgresRejectsInvalidCacheWithoutDemand(t *testing.T) {
	for _, variant := range []string{"expired", "abnormal", "future", "lifetime", "shape", "wrong account shape"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			state := f.state
			state.DemandReason = ""
			switch variant {
			case "expired":
				state.IssuedAt = state.IssuedAt.Add(-service.CodexTurnStateLifetime)
				state.ExpiresAt = state.ExpiresAt.Add(-service.CodexTurnStateLifetime)
			case "abnormal":
				state.EncryptedToken = ""
				state.Shape = "extended"
				state.TokenLength = 312
				state.CipherBlocks = 11
			case "future":
				state.IssuedAt = time.Now().Add(time.Minute)
				state.ExpiresAt = state.IssuedAt.Add(service.CodexTurnStateLifetime)
			case "lifetime":
				state.ExpiresAt = state.ExpiresAt.Add(time.Second)
			case "shape":
				state.Shape = "unknown"
			case "wrong account shape":
				state.TokenLength = 332
				state.CipherBlocks = 12
			}
			ok, err := f.states.SaveCAS(ctx, state, state.Version)
			require.NoError(t, err)
			require.True(t, ok)
			account := f.change(t, ctx, false, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}, nil)
			key := f.key
			key.Generation = service.CodexTurnStateGenerationForAccount(account)
			got, err := f.states.Get(ctx, key)
			require.NoError(t, err)
			require.Nil(t, got, "invalid or missing cache alone must not establish new demand")
		})
	}
}

func TestCodexCollectorProxyChangePostgresRollsBackWithConfiguration(t *testing.T) {
	ctx := context.Background()
	f := newCodexProxyChangeFixture(t)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	_, err = f.admin.UpdateAccount(txCtx, f.key.OwnerAccountID, &service.UpdateAccountInput{CodexTurnState: &service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: &f.proxyID}})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	account, err := f.accounts.GetByID(ctx, f.key.OwnerAccountID)
	require.NoError(t, err)
	account, err = service.ResolveOpenAIOAuthCredentialAccount(ctx, f.accounts, account, f.key.OSFamily)
	require.NoError(t, err)
	require.Equal(t, f.key.Generation, service.CodexTurnStateGenerationForAccount(account))
	state, err := f.states.Get(ctx, f.key)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, f.state.Version, state.Version)
	require.Equal(t, f.state.EncryptedToken, state.EncryptedToken)
	require.Equal(t, f.state.CollectionReason, state.CollectionReason)
}
