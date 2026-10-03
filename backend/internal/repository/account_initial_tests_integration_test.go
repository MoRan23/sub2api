//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func initialTestAccount(t *testing.T, ar *accountRepository, change func(*service.Account)) *service.Account {
	t.Helper()
	a := &service.Account{Name: "initial-synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: oauthOSTestGrant("initial-synthetic"), Extra: map[string]any{}}
	if change != nil {
		change(a)
	}
	require.NoError(t, ar.Create(context.Background(), a))
	t.Cleanup(func() {
		// Candy history deliberately survives account deletion; remove only these
		// synthetic batches so other integration tests keep an empty queue.
		_, _ = integrationDB.Exec(`DELETE FROM account_candy_test_batches WHERE id IN (SELECT batch_id FROM account_candy_test_items WHERE account_id=$1)`, a.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, a.ID)
	})
	return a
}

func initialTestCounts(t *testing.T, id int64) (int, int) {
	t.Helper()
	var attribution, pelican int
	require.NoError(t, integrationDB.QueryRow(`SELECT (SELECT count(*) FROM model_attribution_jobs WHERE account_id=$1),(SELECT count(*) FROM account_candy_test_items WHERE account_id=$1)`, id).Scan(&attribution, &pelican))
	return attribution, pelican
}

func TestAttributionNewAccountsOnceAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	r, ar, existing := attributionFixture(t)
	a := initialTestAccount(t, ar, nil)
	// Normal scheduling cannot beat the initial job's selected model.
	_, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	attr, pelican := initialTestCounts(t, a.ID)
	require.Zero(t, attr)
	require.Zero(t, pelican)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- (&attributionRepository{db: integrationDB}).EnqueueNewAccounts(ctx) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	attr, pelican = initialTestCounts(t, a.ID)
	require.Equal(t, 1, attr)
	require.Equal(t, 1, pelican)
	var source, model, pelicanModel, version string
	require.NoError(t, integrationDB.QueryRow(`SELECT source,snapshot->'policy'->>'model' FROM model_attribution_jobs WHERE account_id=$1`, a.ID).Scan(&source, &model))
	require.Equal(t, "initial", source)
	require.Equal(t, service.AttributionDefaultModel, model)
	require.NoError(t, integrationDB.QueryRow(`SELECT model,prompt_version FROM account_candy_test_items WHERE account_id=$1`, a.ID).Scan(&pelicanModel, &version))
	require.Equal(t, model, pelicanModel)
	require.Equal(t, service.CandyTestPromptVersion, version)
	_, oldPelican := initialTestCounts(t, existing.ID)
	require.Zero(t, oldPelican)
	// Even history cleanup and a fresh service/repository must not recreate it.
	_, err = integrationDB.Exec(`DELETE FROM model_attribution_jobs WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`DELETE FROM account_candy_test_batches WHERE id IN (SELECT batch_id FROM account_candy_test_items WHERE account_id=$1)`, a.ID)
	require.NoError(t, err)
	require.NoError(t, (&attributionRepository{db: integrationDB}).EnqueueNewAccounts(ctx))
	attr, pelican = initialTestCounts(t, a.ID)
	require.Zero(t, attr)
	require.Zero(t, pelican)
}

func TestAttributionNewAccountsIndependentSwitches(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		enabled, attribution, pelican bool
		wantAttribution, wantPelican  int
	}{
		{"both", true, true, true, 1, 1}, {"pelican_only", false, true, true, 0, 1},
		{"attribution_only", true, true, false, 1, 0}, {"no_initial_attribution", true, false, true, 0, 1}, {"both_off", true, false, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, ar, _ := attributionFixture(t)
			c, err := r.Config(ctx)
			require.NoError(t, err)
			c.Enabled = tc.enabled
			c.NewAccountTests = service.NewAccountTestConfig{Attribution: tc.attribution, Pelican: tc.pelican, Model: "gpt-6-sol"}
			c, err = r.SaveConfig(ctx, c)
			require.NoError(t, err)
			a := initialTestAccount(t, ar, nil)
			require.NoError(t, r.EnqueueNewAccounts(ctx))
			attr, pelican := initialTestCounts(t, a.ID)
			require.Equal(t, tc.wantAttribution, attr)
			require.Equal(t, tc.wantPelican, pelican)
			if pelican > 0 {
				var model string
				require.NoError(t, integrationDB.QueryRow(`SELECT model FROM account_candy_test_items WHERE account_id=$1`, a.ID).Scan(&model))
				require.Equal(t, "gpt-6-sol", model)
			}
			if c.Enabled {
				_, err = r.Enqueue(ctx, nil, false)
				require.NoError(t, err)
				attr, _ = initialTestCounts(t, a.ID)
				require.Equal(t, tc.wantAttribution, attr)
			}
			// Turning both on after the event was consumed never backfills a first test.
			c.NewAccountTests.Attribution = true
			c.NewAccountTests.Pelican = true
			_, err = r.SaveConfig(ctx, c)
			require.NoError(t, err)
			require.NoError(t, r.EnqueueNewAccounts(ctx))
			attr, pelican = initialTestCounts(t, a.ID)
			require.Equal(t, tc.wantAttribution, attr)
			require.Equal(t, tc.wantPelican, pelican)
		})
	}
}

func TestAttributionNewAccountsSkipAndManualDedup(t *testing.T) {
	ctx := context.Background()
	r, ar, parent := attributionFixture(t)
	for _, change := range []func(*service.Account){
		func(a *service.Account) { a.Type = service.AccountTypeAPIKey },
		func(a *service.Account) { a.Platform = service.PlatformAnthropic },
		func(a *service.Account) {
			a.ParentAccountID = &parent.ID
			a.QuotaDimension = service.QuotaDimensionSpark
		},
		func(a *service.Account) { a.Schedulable = false },
		func(a *service.Account) { a.Status = service.StatusDisabled },
		func(a *service.Account) { past := time.Now().Add(-time.Hour); a.ExpiresAt = &past },
		func(a *service.Account) { a.Extra["openai_passthrough"] = true },
	} {
		a := initialTestAccount(t, ar, change)
		require.NoError(t, r.EnqueueNewAccounts(ctx))
		attr, pelican := initialTestCounts(t, a.ID)
		require.Zero(t, attr)
		require.Zero(t, pelican)
	}
	a := initialTestAccount(t, ar, nil)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	_, err = NewAccountCandyTestRepository(integrationDB).Create(ctx, &service.CandyTestCreateRequest{AccountIDs: []int64{a.ID}, Model: "gpt-6-sol", IdempotencyKey: "initial-manual-test"}, []*service.CandyTestItem{{AccountID: a.ID, AccountName: a.Name, Status: "queued"}})
	require.NoError(t, err)
	require.NoError(t, r.EnqueueNewAccounts(ctx))
	attr, pelican := initialTestCounts(t, a.ID)
	require.Equal(t, 1, attr)
	require.Equal(t, 1, pelican)
}

func TestAttributionNewAccountGroupPolicyAndBaseline(t *testing.T) {
	ctx := context.Background()
	r, ar, _ := attributionFixture(t)
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "initial-group", Platform: service.PlatformOpenAI})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM groups WHERE id=$1`, g.ID) })
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Groups = []service.AttributionGroupPolicy{{GroupID: g.ID, Enabled: true, AttributionPolicy: service.AttributionPolicy{Model: "periodic-model", HighModels: []string{"group-high"}, LowModels: []string{"group-low"}}}}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	a := &service.Account{Name: "initial-group-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: oauthOSTestGrant("initial-group"), Extra: map[string]any{}}
	require.NoError(t, ar.CreateWithAccountGroups(ctx, a, []service.AccountGroup{{GroupID: g.ID, Priority: 1}}))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM account_candy_test_batches WHERE id IN (SELECT batch_id FROM account_candy_test_items WHERE account_id=$1)`, a.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, a.ID)
	})
	require.NoError(t, r.EnqueueNewAccounts(ctx))
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	require.Equal(t, g.ID, j.Snapshot.GroupID)
	require.Equal(t, service.AttributionDefaultModel, j.Snapshot.Policy.Model)
	require.Equal(t, []string{"group-high"}, j.Snapshot.Policy.HighModels)
	valid, err := r.Validate(ctx, j)
	require.NoError(t, err)
	require.True(t, valid)
	j.Status = "passed"
	require.NoError(t, r.Finish(ctx, j))
	require.Contains(t, attributionMapping(t, ar, a.ID), "group-high")
	var digest string
	require.NoError(t, integrationDB.QueryRow(`SELECT policy_digest FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&digest))
	require.Equal(t, service.AttributionDigest([]any{g.ID, j.Snapshot.Policy}), digest)
	j = attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "periodic-model", j.Snapshot.Policy.Model)
	require.NoError(t, integrationDB.QueryRow(`SELECT policy_digest FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&digest))
	require.Equal(t, service.AttributionDigest([]any{g.ID, j.Snapshot.Policy}), digest)
}

func TestAttributionNewAccountCreationRollsBackWithEvent(t *testing.T) {
	_, ar, _ := attributionFixture(t)
	_, err := integrationDB.Exec(`CREATE FUNCTION initial_event_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic event failure'; END $$; CREATE TRIGGER initial_event_fail BEFORE INSERT ON account_initial_tests FOR EACH ROW EXECUTE FUNCTION initial_event_fail()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DROP TRIGGER IF EXISTS initial_event_fail ON account_initial_tests; DROP FUNCTION IF EXISTS initial_event_fail()`)
	})
	a := &service.Account{Name: "initial-rollback", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: oauthOSTestGrant("rollback")}
	require.Error(t, ar.Create(context.Background(), a))
	var exists bool
	require.NoError(t, integrationDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`, a.ID).Scan(&exists))
	require.False(t, exists)
}

func TestAttributionNewAccountsAtomicEnqueueAndDisableBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	r, ar, _ := attributionFixture(t)
	a := initialTestAccount(t, ar, nil)
	// A queue persistence failure must roll back the other queue and marker.
	_, err := integrationDB.Exec(`CREATE FUNCTION initial_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic queue failure'; END $$; CREATE TRIGGER initial_test_fail BEFORE INSERT ON account_candy_test_items FOR EACH ROW EXECUTE FUNCTION initial_test_fail()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DROP TRIGGER IF EXISTS initial_test_fail ON account_candy_test_items; DROP FUNCTION IF EXISTS initial_test_fail()`)
	})
	require.Error(t, r.EnqueueNewAccounts(ctx))
	attr, pelican := initialTestCounts(t, a.ID)
	require.Zero(t, attr)
	require.Zero(t, pelican)
	var pending bool
	require.NoError(t, integrationDB.QueryRow(`SELECT processed_at IS NULL FROM account_initial_tests WHERE account_id=$1`, a.ID).Scan(&pending))
	require.True(t, pending)
	_, err = integrationDB.Exec(`DROP TRIGGER initial_test_fail ON account_candy_test_items; DROP FUNCTION initial_test_fail()`)
	require.NoError(t, err)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.NewAccountTests.Attribution = false
	c.NewAccountTests.Pelican = false
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.NoError(t, r.EnqueueNewAccounts(ctx))
	attr, pelican = initialTestCounts(t, a.ID)
	require.Zero(t, attr)
	require.Zero(t, pelican)
}
