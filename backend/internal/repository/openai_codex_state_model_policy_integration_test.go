//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Tests touching global policy run sequentially and restore both original keys.
// nil leaves the policy absent, exercising the virtual default without a seed.
func installCodexStateModelPolicyFixture(t *testing.T, models []string) string {
	t.Helper()
	ctx := context.Background()
	repo := NewSettingRepository(testEntClient(t))
	keys := []string{service.SettingKeyCodexTurnStateModels, service.SettingKeyCodexTurnStateModelsRevision}
	original, err := repo.GetMultiple(ctx, keys)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM settings WHERE key IN ($1,$2)`, keys[0], keys[1])
		require.NoError(t, err)
		require.NoError(t, repo.SetMultiple(context.Background(), original))
	})
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key IN ($1,$2)`, keys[0], keys[1])
	require.NoError(t, err)
	if models != nil {
		raw, err := json.Marshal(models)
		require.NoError(t, err)
		require.NoError(t, repo.SetMultiple(ctx, map[string]string{keys[0]: string(raw), keys[1]: uuid.NewString()}))
	}
	return codexStateModelPolicyRevisionForTest(t)
}

func codexStateModelPolicyRevisionForTest(t *testing.T) string {
	t.Helper()
	values, err := NewSettingRepository(testEntClient(t)).GetMultiple(context.Background(), []string{
		service.SettingKeyCodexTurnStateModels, service.SettingKeyCodexTurnStateModelsRevision,
	})
	require.NoError(t, err)
	_, revision, err := service.ParseCodexTurnStateModelPolicyValues(values)
	require.NoError(t, err)
	return revision
}

func newCodexStatePolicyCandidate(t *testing.T, ctx context.Context, repo service.CodexTurnStateRepository, key service.CodexTurnStateKey, revision string) *service.CodexTurnStateRecord {
	t.Helper()
	now := time.Now().UTC()
	record, err := repo.BeginBusiness(ctx, key, "policy-attempt", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record)
	record.ModelPolicyRevision = revision
	record.EncryptedToken, record.Source = "synthetic-policy-ciphertext", "business"
	record.BundleBinding = service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct"}
	record.IssuedAt, record.ExpiresAt = now, now.Add(service.CodexTurnStateLifetime)
	return record
}

func TestCodexStateModelPolicyPostgresVirtualDefaultAndTransientRevision(t *testing.T) {
	key := createCodexStateFixture(t)
	key.Model = service.DefaultCodexTurnStateModels()[0]
	revision := installCodexStateModelPolicyFixture(t, nil)
	ctx := context.Background()
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	record := newCodexStatePolicyCandidate(t, ctx, repo, key, revision)
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
	loaded, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, record.EncryptedToken, loaded.EncryptedToken)
	require.Empty(t, loaded.ModelPolicyRevision, "policy revision belongs to the physical attempt, not the persistent token")
	var value string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyCodexTurnStateModelsRevision).Scan(&value))
	require.Empty(t, value, "the lock placeholder must preserve the virtual default revision")
	saved, err = repo.SaveCAS(ctx, *loaded, loaded.Version)
	require.NoError(t, err)
	require.False(t, saved, "a caller without a frozen policy must not publish")
}

func TestCodexStateModelPolicyPostgresPendingExclusionFencesPublication(t *testing.T) {
	key := createCodexStateFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	record := newCodexStatePolicyCandidate(t, ctx, repo, key, codexStateModelPolicyRevisionForTest(t))
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE settings SET value=CASE key WHEN $1 THEN '[]' ELSE $3 END
		WHERE key IN ($1,$2)`, service.SettingKeyCodexTurnStateModels, service.SettingKeyCodexTurnStateModelsRevision, uuid.NewString())
	require.NoError(t, err)
	type outcome struct {
		saved bool
		err   error
	}
	done := make(chan outcome, 1)
	go func() { saved, err := repo.SaveCAS(ctx, *record, record.Version); done <- outcome{saved, err} }()
	select {
	case value := <-done:
		t.Fatalf("publication escaped the pending policy write: %+v", value)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	result := <-done
	require.NoError(t, result.err)
	require.False(t, result.saved)
	loaded, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, loaded.EncryptedToken)
	require.Equal(t, record.Version, loaded.Version)
}

func TestCodexStateModelPolicyPostgresPublicationBlocksPolicyCommit(t *testing.T) {
	key := createCodexStateFixture(t)
	key.Model = service.DefaultCodexTurnStateModels()[0]
	installCodexStateModelPolicyFixture(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record := service.CodexTurnStateRecord{OSFamily: "windows", OwnerAccountID: key.OwnerAccountID, Model: key.Model,
		Generation: key.Generation, ModelPolicyRevision: codexStateModelPolicyRevisionForTest(t)}
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	valid, err := lockCodexStateModelPolicy(ctx, tx, record)
	require.NoError(t, err)
	require.True(t, valid)
	settings := NewSettingRepository(testEntClient(t))
	done := make(chan error, 1)
	go func() {
		done <- settings.SetMultiple(ctx, map[string]string{
			service.SettingKeyCodexTurnStateModels: "[]", service.SettingKeyCodexTurnStateModelsRevision: uuid.NewString(),
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("policy write escaped the publication barrier: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	require.NoError(t, <-done)
}

func TestCodexStateModelPolicyPostgresCurrentRevisionStillRequiresMembership(t *testing.T) {
	key := createCodexStateFixture(t)
	revision := installCodexStateModelPolicyFixture(t, []string{})
	ctx := context.Background()
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	record := newCodexStatePolicyCandidate(t, ctx, repo, key, revision)
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.False(t, saved)
	loaded, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, loaded.EncryptedToken)
	require.Equal(t, record.Version, loaded.Version)
}

func TestCodexStateModelPolicyPostgresRestoredListRejectsOldAttempt(t *testing.T) {
	key := createCodexStateFixture(t)
	ctx := context.Background()
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	record := newCodexStatePolicyCandidate(t, ctx, repo, key, codexStateModelPolicyRevisionForTest(t))
	settings := NewSettingRepository(testEntClient(t))
	for _, raw := range []string{"[]", `["gpt-5.4"]`} {
		require.NoError(t, settings.SetMultiple(ctx, map[string]string{
			service.SettingKeyCodexTurnStateModels: raw, service.SettingKeyCodexTurnStateModelsRevision: uuid.NewString(),
		}))
	}
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.False(t, saved)
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	saved, err = repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
}

func TestSettingRepositoryConcurrentModelPairsStayAtomic(t *testing.T) {
	installCodexStateModelPolicyFixture(t, []string{"writer0"})
	settings := NewSettingRepository(testEntClient(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := range 25 {
				model := fmt.Sprintf("writer%d", i)
				if err := settings.SetMultiple(ctx, map[string]string{
					service.SettingKeyCodexTurnStateModels:         fmt.Sprintf("[\"%s\"]", model),
					service.SettingKeyCodexTurnStateModelsRevision: fmt.Sprintf("%s-%d", model, n),
				}); err != nil {
					errCh <- err
					return
				}
				values, err := settings.GetMultiple(ctx, []string{service.SettingKeyCodexTurnStateModels, service.SettingKeyCodexTurnStateModelsRevision})
				if err != nil {
					errCh <- err
					return
				}
				var models []string
				if json.Unmarshal([]byte(values[service.SettingKeyCodexTurnStateModels]), &models) != nil || len(models) != 1 ||
					!strings.HasPrefix(values[service.SettingKeyCodexTurnStateModelsRevision], models[0]+"-") {
					errCh <- fmt.Errorf("torn policy pair: %v", values)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}
