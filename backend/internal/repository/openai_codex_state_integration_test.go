//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func createCodexStateFixture(t *testing.T) service.CodexTurnStateKey {
	t.Helper()
	installCodexStateModelPolicyFixture(t, []string{"gpt-5.4"})
	var ownerID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO accounts
		(name, platform, type, credentials, extra) VALUES ($1,'openai','oauth','{}',
		'{"codex_turn_state":{"enabled":true,"account_type":"personal"},"codex_turn_state_generation":"00000000-0000-4000-8000-000000000001"}'::jsonb)
		RETURNING id`, t.Name()).Scan(&ownerID))
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO account_openai_oauth_credentials
		(account_id,credentials,status,credential_epoch) VALUES
		($1,'{"access_token":"fixture-token","plan_type":"plus"}','authorized','00000000-0000-4000-8000-000000000011')`, ownerID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `INSERT INTO account_openai_oauth_os_credentials
		(account_id,os_family,credentials,status,state_generation,credential_epoch,authorization_generation)
		SELECT account_id,'windows','{}','authorized','00000000-0000-4000-8000-000000000001',credential_epoch,authorization_generation
		FROM account_openai_oauth_credentials WHERE account_id=$1`, ownerID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, ownerID)
	})
	return service.CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: ownerID, Model: "gpt-5.4", Generation: "00000000-0000-4000-8000-000000000001"}
}

func TestCodexStatePostgresNaturalLeasesDurabilityAndCAS(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	first := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	second := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := first.BeginBusiness(ctx, key, "attempt-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record)
	require.EqualValues(t, 1, record.Version)
	require.True(t, record.BusinessInFlight)
	require.Equal(t, time.Unix(0, 0).UTC(), record.LastBusinessAt, "preparation is not physical business activity")
	require.NoError(t, first.MarkBusinessSent(ctx, key, now))
	// A second physical request preserves token version and both leases remain visible.
	another, err := second.BeginBusiness(ctx, key, "attempt-b", now.Add(time.Second), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, record.Version, another.Version)
	require.NoError(t, second.MarkBusinessSent(ctx, key, now.Add(time.Second)))
	require.NoError(t, first.EndBusiness(ctx, key, "attempt-a"))
	active, err := first.HasBusiness(ctx, key, now)
	require.NoError(t, err)
	require.True(t, active)
	withLease, err := second.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, withLease.BusinessInFlight, "another instance must observe a remaining live business lease")
	active, err = first.HasBusiness(ctx, key, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, active, "abandoned leases must expire after process loss")
	require.NoError(t, second.EndBusiness(ctx, key, "attempt-b"))
	withoutLease, err := first.Get(ctx, key)
	require.NoError(t, err)
	require.False(t, withoutLease.BusinessInFlight)

	record.EncryptedToken = "ciphertext-from-secret-encryptor"
	record.IssuedAt, record.ExpiresAt = now, now.Add(service.CodexTurnStateLifetime)
	record.TokenLength, record.CipherBlocks = 292, 10
	record.Source, record.Shape = "business", "accepted"
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	saved, err := first.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
	// Reconstructing the repository cannot lose or renew persisted token state.
	restarted := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	loaded, err := restarted.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, record.EncryptedToken, loaded.EncryptedToken)
	require.Equal(t, now, loaded.IssuedAt)
	require.Equal(t, now.Add(service.CodexTurnStateLifetime), loaded.ExpiresAt)
	require.Equal(t, now.Add(time.Second), loaded.LastBusinessAt, "a slow write must not regress recent activity")
	require.Equal(t, record.Version+1, loaded.Version)
	// A probe/abnormal response holding the old version cannot erase the new token.
	record.EncryptedToken = ""
	saved, err = second.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.False(t, saved)
	loaded, err = second.Get(ctx, key)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.EncryptedToken)
	list, err := restarted.ListByAccount(ctx, key.OwnerAccountID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	list, err = restarted.ListActive(ctx, now.Add(2*time.Second), 1000)
	require.NoError(t, err)
	for _, item := range list {
		require.NotEqual(t, key.OwnerAccountID, item.OwnerAccountID)
	}
}

func TestCodexStatePostgresConcurrentCASAndGenerationFence(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "attempt", now, now.Add(time.Minute))
	require.NoError(t, err)
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	const writers = 12
	var wins atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := *record
			candidate.EncryptedToken = fmt.Sprintf("encrypted-candidate-%d", i)
			candidate.IssuedAt, candidate.ExpiresAt = now, now.Add(service.CodexTurnStateLifetime)
			ok, err := repo.SaveCAS(ctx, candidate, record.Version)
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, wins.Load())
	loaded, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.EncryptedToken)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET state_generation='00000000-0000-4000-8000-000000000002'
		WHERE account_id=$1 AND os_family='windows'`, key.OwnerAccountID)
	require.NoError(t, err)
	missing, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, missing)
	loaded.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	saved, err := repo.SaveCAS(ctx, *loaded, loaded.Version)
	require.NoError(t, err)
	require.False(t, saved)
	oldBegin, err := repo.BeginBusiness(ctx, key, "late-attempt", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.Nil(t, oldBegin)
	key.Generation = "00000000-0000-4000-8000-000000000002"
	reset, err := repo.BeginBusiness(ctx, key, "new-attempt", now.Add(time.Second), now.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, reset.EncryptedToken)
	require.Greater(t, reset.Version, loaded.Version)
	require.False(t, reset.CollectorPaused)
	require.True(t, reset.IssuedAt.IsZero())
	var leases int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM openai_codex_state_business_leases
		WHERE owner_account_id=$1`, key.OwnerAccountID).Scan(&leases))
	require.Equal(t, 1, leases, "the generation reset removes prior generation leases")
}

func TestCodexStatePostgresDisableSerializesWithPublication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	record, err := repo.BeginBusiness(ctx, key, "attempt", now, now.Add(time.Minute))
	require.NoError(t, err)
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state,enabled}', 'false'::jsonb) WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	type result struct {
		saved bool
		err   error
	}
	publication := make(chan result, 1)
	go func() { ok, err := repo.SaveCAS(ctx, *record, record.Version); publication <- result{ok, err} }()
	// Until the canonical account write commits, publication must wait on its lock.
	select {
	case result := <-publication:
		t.Fatalf("publication escaped pending configuration lock: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	outcome := <-publication
	require.NoError(t, outcome.err)
	require.False(t, outcome.saved)
	missing, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, missing)
	rows, err := repo.ListByAccount(ctx, key.OwnerAccountID)
	require.NoError(t, err)
	require.Empty(t, rows)
	active, err := repo.HasBusiness(ctx, key, now)
	require.NoError(t, err)
	require.False(t, active)
}

func TestCodexStateRedisIntegrationAccountLock(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	first := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	second := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	ok, err := first.AcquireCollector(ctx, key.OwnerAccountID, "first", time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	t.Cleanup(func() { _ = first.ReleaseCollector(context.Background(), key.OwnerAccountID, "first") })
	require.NoError(t, second.ReleaseCollector(ctx, key.OwnerAccountID, "wrong-owner"))
	ok, err = second.AcquireCollector(ctx, key.OwnerAccountID, "second", time.Second)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, first.ReleaseCollector(ctx, key.OwnerAccountID, "first"))
	ok, err = second.AcquireCollector(ctx, key.OwnerAccountID, "second", time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, second.ReleaseCollector(ctx, key.OwnerAccountID, "second"))
}

func TestCodexStatePostgresScanSelectsOnlyDueCollectors(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state,collector_proxy_id}', '42'::jsonb) WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND os_family=$2`, key.OwnerAccountID, key.OSFamily).Scan(&key.Generation))
	now := time.Now().UTC().Truncate(time.Microsecond)
	policyRevision := installCodexStateModelPolicyFixture(t, []string{"fresh", "due", "missing", "demand", "paused", "cooldown", "natural-inflight"})
	for _, model := range []string{"fresh", "due", "missing", "demand", "paused", "cooldown", "natural-inflight"} {
		modelKey := key
		modelKey.Model = model
		record, err := repo.BeginBusiness(ctx, modelKey, model, now, now.Add(time.Minute))
		require.NoError(t, err)
		require.NoError(t, repo.MarkBusinessSent(ctx, modelKey, now))
		record.ModelPolicyRevision = policyRevision
		if model != "natural-inflight" {
			require.NoError(t, repo.EndBusiness(ctx, modelKey, model))
		}
		switch model {
		case "fresh":
			record.EncryptedToken, record.IssuedAt, record.ExpiresAt = "encrypted", now, now.Add(service.CodexTurnStateLifetime)
		case "due":
			record.EncryptedToken, record.IssuedAt, record.ExpiresAt = "encrypted", now.Add(-service.CodexTurnStateLifetime+20*time.Second), now.Add(20*time.Second)
		case "demand", "natural-inflight":
			record.DemandReason, record.DemandAt = "extended_shape", now
		case "paused":
			record.CollectorPaused = true
			record.DemandReason, record.DemandAt = "extended_shape", now
		case "cooldown":
			record.NextCollectAt = now.Add(3 * time.Minute)
			record.DemandReason, record.DemandAt = "extended_shape", now
		}
		saved, err := repo.SaveCAS(ctx, *record, record.Version)
		require.NoError(t, err)
		require.True(t, saved)
	}
	records, err := repo.ListActive(ctx, now.Add(-time.Minute), 1000)
	require.NoError(t, err)
	var models []string
	for _, record := range records {
		if record.OwnerAccountID == key.OwnerAccountID {
			models = append(models, record.Model)
		}
	}
	require.ElementsMatch(t, []string{"due", "demand", "natural-inflight"}, models, "due collection continues alongside business leases")
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state,collector_proxy_id}', 'null'::jsonb) WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	records, err = repo.ListActive(ctx, now.Add(-time.Minute), 1000)
	require.NoError(t, err)
	for _, record := range records {
		require.NotEqual(t, key.OwnerAccountID, record.OwnerAccountID)
	}
}
