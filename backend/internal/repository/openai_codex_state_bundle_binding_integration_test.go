//go:build integration

package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newCodexBoundProxyFixture(t *testing.T) (service.CodexTurnStateRepository, service.CodexTurnStateKey, *service.CodexTurnStateRecord, *service.Proxy) {
	t.Helper()
	ctx := context.Background()
	key := createCodexStateFixture(t)
	proxy := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "bound-route"})
	proxy, err := NewProxyRepository(integrationEntClient, integrationDB).GetByID(ctx, proxy.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, key.OwnerAccountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM proxies WHERE id=$1`, proxy.ID)
	})
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state,collector_proxy_ids}',jsonb_build_array($2::bigint)) WHERE id=$1`, key.OwnerAccountID, proxy.ID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_credentials WHERE account_id=$1`, key.OwnerAccountID).Scan(&key.Generation))
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "bundle", now, now.Add(time.Minute))
	require.NoError(t, err)
	record.EncryptedToken, record.EncryptedCookieBundle = "ticket", "cookie-snapshot"
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.IssuedAt, record.ExpiresAt = now, now.Add(service.CodexTurnStateLifetime)
	record.TokenLength, record.CipherBlocks, record.Shape = 292, 10, "target"
	record.BundleBinding = service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "proxy", ProxyID: proxy.ID, ProxyRouteGeneration: proxy.RouteGeneration}
	return repo, key, record, proxy
}

func TestCodexStateBundleBindingCASPublishesAndRejectsUnknownOrUnavailableRoute(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{"missing", "compact", "generation", "disabled", "expired", "removed", "valid"} {
		t.Run(mutation, func(t *testing.T) {
			repo, key, record, proxy := newCodexBoundProxyFixture(t)
			switch mutation {
			case "missing":
				record.BundleBinding = service.CodexTurnStateBundleBinding{}
			case "compact":
				record.BundleBinding.WireMode = "compact"
			case "generation":
				record.BundleBinding.ProxyRouteGeneration++
			case "disabled":
				_, err := integrationDB.ExecContext(ctx, `UPDATE proxies SET status='inactive' WHERE id=$1`, proxy.ID)
				require.NoError(t, err)
			case "expired":
				_, err := integrationDB.ExecContext(ctx, `UPDATE proxies SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, proxy.ID)
				require.NoError(t, err)
			case "removed":
				// Test the independent allowlist fence without relying on the
				// generation trigger that ordinarily rejects this race first.
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,collector_proxy_ids}','[]') WHERE id=$1`, key.OwnerAccountID)
				require.NoError(t, err)
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_credentials WHERE account_id=$1`, key.OwnerAccountID).Scan(&key.Generation))
				fresh, err := repo.BeginBusiness(ctx, key, "removed", time.Now(), time.Now().Add(time.Minute))
				require.NoError(t, err)
				record.Generation, record.Version = fresh.Generation, fresh.Version
			}
			saved, err := repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.Equal(t, mutation == "valid", saved)
			stored, err := repo.Get(ctx, key)
			require.NoError(t, err)
			require.NotNil(t, stored)
			if saved {
				require.Equal(t, record.BundleBinding, stored.BundleBinding)
				require.Equal(t, record.EncryptedToken, stored.EncryptedToken)
				require.Equal(t, record.EncryptedCookieBundle, stored.EncryptedCookieBundle)
			} else {
				require.Empty(t, stored.EncryptedToken)
				require.Empty(t, stored.EncryptedCookieBundle)
				require.Equal(t, service.CodexTurnStateBundleBinding{}, stored.BundleBinding)
			}
		})
	}
}

func TestCodexStateBundleCASWaitsForRouteChangeAndRejectsLateResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, _, record, proxy := newCodexBoundProxyFixture(t)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE proxies SET host='changed.invalid',route_generation=route_generation+1 WHERE id=$1`, proxy.ID)
	require.NoError(t, err)
	type outcome struct {
		saved bool
		err   error
	}
	result := make(chan outcome, 1)
	go func() { saved, err := repo.SaveCAS(ctx, *record, record.Version); result <- outcome{saved, err} }()
	select {
	case early := <-result:
		t.Fatalf("publication escaped pending proxy identity change: %+v", early)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	late := <-result
	require.NoError(t, late.err)
	require.False(t, late.saved)
}

func TestCodexStateBundleCASRechecksExpiryAfterWaitingForAccountLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, key, record, proxy := newCodexBoundProxyFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE proxies SET expires_at=clock_timestamp()+INTERVAL '300 milliseconds' WHERE id=$1`, proxy.ID)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, key.OwnerAccountID)
	require.NoError(t, err)
	type outcome struct {
		saved bool
		err   error
	}
	result := make(chan outcome, 1)
	go func() { saved, err := repo.SaveCAS(ctx, *record, record.Version); result <- outcome{saved, err} }()
	select {
	case early := <-result:
		t.Fatalf("publication escaped pending account lock: %+v", early)
	case <-time.After(350 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	late := <-result
	require.NoError(t, late.err)
	require.False(t, late.saved, "transaction start time cannot extend proxy validity")
}

func TestCodexStateCollectorActivityRequiresEligiblePhysicalRequest(t *testing.T) {
	ctx := context.Background()
	repo, key, _, _ := newCodexBoundProxyFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.MarkBusinessSent(ctx, key, now))
	containsOwner := func() bool {
		rows, err := repo.ListActive(ctx, now.Add(-time.Minute), 10000)
		require.NoError(t, err)
		for _, row := range rows {
			if row.OwnerAccountID == key.OwnerAccountID {
				return true
			}
		}
		return false
	}
	require.False(t, containsOwner(), "compact and legacy business activity cannot start collection")
	require.NoError(t, repo.MarkEligibleCollectionSent(ctx, key, now))
	require.True(t, containsOwner())
	require.NoError(t, repo.MarkEligibleCollectionSent(ctx, key, now.Add(-time.Minute)))
	stored, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, now, stored.LastEligibleCollectionAt, "late marks cannot regress activity")
	require.Equal(t, now, stored.LastBusinessAt)
}

func TestCodexStateCollectorListChangeUsesPublishedBundleRoute(t *testing.T) {
	for _, keep := range []bool{false, true} {
		name := "remove published route"
		if keep {
			name = "keep published route behind new collector"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newCodexProxyChangeFixture(t)
			publishedProxy := service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(f.account))[0]
			var generation int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT route_generation FROM proxies WHERE id=$1`, publishedProxy).Scan(&generation))
			record := f.state
			record.BundleBinding = service.CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: publishedProxy, ProxyRouteGeneration: generation}
			// The current rotation cursor is deliberately a different route.
			record.CollectorProxyID = f.proxyID
			saved, err := f.states.SaveCAS(ctx, record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			ids := []int64{f.proxyID}
			if keep {
				ids = append(ids, publishedProxy)
			}
			account := f.change(t, ctx, false, service.CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyIDs: ids}, nil)
			key := f.key
			key.Generation = service.CodexTurnStateGenerationForAccount(account)
			stored, err := f.states.Get(ctx, key)
			require.NoError(t, err)
			require.NotNil(t, stored)
			if keep {
				require.Equal(t, record.EncryptedToken, stored.EncryptedToken)
				require.Equal(t, record.BundleBinding, stored.BundleBinding)
			} else {
				require.Empty(t, stored.EncryptedToken)
				require.Empty(t, stored.EncryptedCookieBundle)
				require.Equal(t, service.CodexTurnStateBundleBinding{}, stored.BundleBinding)
			}
		})
	}
}

func TestCodexStateBundleMigrationColdStartsWithoutInventingEligibleActivity(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE proxies (id BIGINT);
		CREATE TEMP TABLE accounts (id BIGINT PRIMARY KEY,codex_turn_state_retry_after TIMESTAMPTZ);
		CREATE TEMP TABLE openai_codex_state (LIKE public.openai_codex_state INCLUDING DEFAULTS);
		ALTER TABLE openai_codex_state DROP COLUMN bundle_wire_mode,DROP COLUMN bundle_egress_kind,
		 DROP COLUMN bundle_proxy_id,DROP COLUMN bundle_proxy_route_generation,DROP COLUMN last_eligible_collection_at,
		 DROP COLUMN bundle_invalidation_version;
		CREATE TEMP TABLE openai_codex_state_business_leases (attempt TEXT);
		CREATE TEMP TABLE openai_http_cookies (cookie TEXT);
		INSERT INTO proxies VALUES (1);
		INSERT INTO accounts VALUES (1,NULL),(2,NULL),(3,NOW()+INTERVAL '2 hours');
		INSERT INTO openai_codex_state(owner_account_id,model,generation,authorization_generation,version,
		 encrypted_token,encrypted_cookie_bundle,last_business_at,history_proof_observed_at,
		 next_collect_at,last_error,collection_status,collector_attempt_id,collector_paused,collection_reason)
		VALUES (1,'gpt-5.4','generation',gen_random_uuid(),5,'ticket','cookies',NOW(),NOW()-INTERVAL '1 minute',
		 NOW()+INTERVAL '3 hours','collector_rate_limited','collecting','00000000-0000-4000-8000-000000000001',FALSE,'collecting'),
		 (2,'gpt-5.4','generation',gen_random_uuid(),6,'ticket','cookies',NOW(),NOW()-INTERVAL '1 minute',
		 NOW()+INTERVAL '3 hours','collector_auth_rejected','paused',NULL,TRUE,'collector_auth_rejected'),
		 (3,'gpt-5.4','generation',gen_random_uuid(),7,'ticket','cookies',NOW(),NOW()-INTERVAL '1 minute',
		 NOW()+INTERVAL '3 hours','collector_rate_limited','backoff',NULL,FALSE,'collector_rate_limited');
		INSERT INTO openai_codex_state_business_leases VALUES ('lease');
		INSERT INTO openai_http_cookies VALUES ('cookie');`)
	require.NoError(t, err)
	var now time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT NOW()`).Scan(&now))
	migration, err := os.ReadFile("../../migrations/258_openai_codex_state_bundle_binding.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for owner := 1; owner <= 3; owner++ {
		var token, cookies, attempt, status, reason string
		var eligible, next sql.NullTime
		var business, history time.Time
		var version int64
		var paused bool
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT encrypted_token,encrypted_cookie_bundle,
		 COALESCE(collector_attempt_id::text,''),collection_status,collection_reason,last_eligible_collection_at,
		 next_collect_at,last_business_at,history_proof_observed_at,version,collector_paused
		 FROM openai_codex_state WHERE owner_account_id=$1`, owner).Scan(&token, &cookies, &attempt, &status, &reason, &eligible, &next, &business, &history, &version, &paused))
		require.Empty(t, token)
		require.Empty(t, cookies)
		require.Empty(t, attempt)
		require.False(t, eligible.Valid)
		require.Equal(t, now, business)
		require.Equal(t, now.Add(-time.Minute), history)
		require.EqualValues(t, owner+5, version)
		switch owner {
		case 1:
			require.False(t, next.Valid, "crash reservation must not become a cooldown")
			require.Equal(t, "idle", status)
		case 2:
			require.True(t, paused)
			require.Equal(t, "paused", status)
			require.Equal(t, "collector_auth_rejected", reason)
		case 3:
			require.Equal(t, now.Add(3*time.Hour), next.Time)
			require.Equal(t, "backoff", status)
		}
	}
	for _, table := range []string{"openai_codex_state_business_leases", "openai_http_cookies"} {
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count)
	}
}
