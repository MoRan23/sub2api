package service

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexModelsSettingsRepo struct {
	*openAIUUIDv7RuntimeRepo
	writeErr    error
	readbackErr error
	writes      []map[string]string
}

func newCodexModelsSettingsRepo(values map[string]string) *codexModelsSettingsRepo {
	if values == nil {
		values = make(map[string]string)
	}
	return &codexModelsSettingsRepo{openAIUUIDv7RuntimeRepo: &openAIUUIDv7RuntimeRepo{values: values}}
}

func (r *codexModelsSettingsRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr
	}
	r.writes = append(r.writes, maps.Clone(values))
	maps.Copy(r.values, values)
	return nil
}

func (r *codexModelsSettingsRepo) GetAll(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.values), r.readbackErr
}

func TestCodexTurnStateModelsNormalize(t *testing.T) {
	normalized, err := NormalizeCodexTurnStateModels([]string{" gpt-6-astra ", "gpt-6-astra", "GPT-6-astra", "vendor/model:v1"})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-6-astra", "GPT-6-astra", "vendor/model:v1"}, normalized)
	empty, err := NormalizeCodexTurnStateModels(nil)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)
	for _, id := range []string{"", " ", "gpt*", "gpt?", "gpt[1]", "gpt{1}", "gpt 6", "gpt\x00", "\ngpt", "gpt\t", strings.Repeat("a", CodexTurnStateModelMaxBytes+1)} {
		t.Run(id, func(t *testing.T) {
			_, err := NormalizeCodexTurnStateModels([]string{id})
			require.Error(t, err)
		})
	}
	_, err = NormalizeCodexTurnStateModels(make([]string, CodexTurnStateModelsMaxCount+1))
	require.Error(t, err)
}

func TestCodexTurnStateModelsMissingDefaultsEmptyDeniesAll(t *testing.T) {
	ctx := context.Background()
	repo := newCodexModelsSettingsRepo(nil)
	svc := NewSettingService(repo, &config.Config{})
	models, revision, err := svc.CodexTurnStateModelPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultCodexTurnStateModels(), models)
	require.NotEmpty(t, revision)
	models[0] = "mutable"
	for _, id := range DefaultCodexTurnStateModels() {
		allowed, err := svc.CodexTurnStateModelAllowed(ctx, id)
		require.NoError(t, err)
		require.True(t, allowed)
	}
	for _, id := range []string{"GPT-6-astra", "gpt-6-astra-mini", "gpt-6", " gpt-6-astra", ""} {
		allowed, err := svc.CodexTurnStateModelAllowed(ctx, id)
		require.NoError(t, err)
		require.False(t, allowed)
	}
	require.Equal(t, int32(1), repo.getCalls.Load())
	repo.mu.Lock()
	repo.values[SettingKeyCodexTurnStateModels] = "[]"
	repo.mu.Unlock()
	models, _, err = svc.CodexTurnStateModelPolicyAuthoritative(ctx)
	require.NoError(t, err)
	require.NotNil(t, models)
	require.Empty(t, models)
	allowed, err := svc.CodexTurnStateModelAllowed(ctx, "gpt-6-astra")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestCodexTurnStateModelsInvalidStorageFailsClosed(t *testing.T) {
	for _, value := range []string{"", "null", "{}", "\"gpt-6-astra\"", "[1]", `["*"]`, `[""]`} {
		t.Run(value, func(t *testing.T) {
			svc := NewSettingService(newCodexModelsSettingsRepo(map[string]string{SettingKeyCodexTurnStateModels: value}), &config.Config{})
			allowed, err := svc.CodexTurnStateModelAllowed(context.Background(), "gpt-6-astra")
			require.ErrorIs(t, err, ErrCodexTurnStateModelsUnavailable)
			require.False(t, allowed)
			require.Equal(t, []string{}, svc.parseSettings(map[string]string{SettingKeyCodexTurnStateModels: value}).CodexTurnStateModels)
		})
	}
}

func TestCodexTurnStateModelPolicyStorageFailureNeverUsesDefaults(t *testing.T) {
	ctx := context.Background()
	repo := newCodexModelsSettingsRepo(nil)
	repo.err = errors.New("database unavailable")
	svc := NewSettingService(repo, &config.Config{})
	for i := 0; i < 2; i++ {
		allowed, err := svc.CodexTurnStateModelAllowed(ctx, "gpt-6-astra")
		require.ErrorIs(t, err, ErrCodexTurnStateModelsUnavailable)
		require.False(t, allowed)
	}
	require.Equal(t, int32(1), repo.getCalls.Load(), "briefly cache the error")
	repo.mu.Lock()
	repo.err = nil
	repo.mu.Unlock()
	models, _, err := svc.CodexTurnStateModelPolicyAuthoritative(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultCodexTurnStateModels(), models)
	repo.mu.Lock()
	repo.err = errors.New("database unavailable again")
	repo.mu.Unlock()
	models, _, err = svc.CodexTurnStateModelPolicyAuthoritative(ctx)
	require.ErrorIs(t, err, ErrCodexTurnStateModelsUnavailable)
	require.Nil(t, models)
	allowed, err := svc.CodexTurnStateModelAllowed(ctx, "gpt-6-astra")
	require.Error(t, err)
	require.False(t, allowed, "failed authoritative read must also deny cached use")
}

func TestCodexTurnStateModelPolicySingleflight(t *testing.T) {
	repo := newCodexModelsSettingsRepo(nil)
	svc := NewSettingService(repo, &config.Config{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			models, _, err := svc.CodexTurnStateModelPolicy(context.Background())
			if err != nil || len(models) != 3 {
				t.Errorf("unexpected policy: %v, %v", models, err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), repo.getCalls.Load())
}

func TestCodexTurnStateModelPolicyCommittedWriteWinsStaleRead(t *testing.T) {
	for _, authoritative := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "authoritative"}[authoritative], func(t *testing.T) {
			repo := newCodexModelsSettingsRepo(nil)
			repo.firstStarted = make(chan struct{})
			repo.firstRelease = make(chan struct{})
			svc := NewSettingService(repo, &config.Config{})
			done := make(chan []string, 1)
			go func() {
				read := svc.CodexTurnStateModelPolicy
				if authoritative {
					read = svc.CodexTurnStateModelPolicyAuthoritative
				}
				models, _, _ := read(context.Background())
				done <- models
			}()
			<-repo.firstStarted
			_, err := svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyCodexTurnStateModels: "[]"}, nil)
			require.NoError(t, err)
			close(repo.firstRelease)
			select {
			case models := <-done:
				require.Equal(t, []string{}, models)
			case <-time.After(time.Second):
				t.Fatal("policy read remained blocked")
			}
		})
	}
}

func TestCodexTurnStateModelPolicySharedStoreAuthoritativeRefresh(t *testing.T) {
	ctx := context.Background()
	repo := newCodexModelsSettingsRepo(nil)
	writer, reader := NewSettingService(repo, &config.Config{}), NewSettingService(repo, &config.Config{})
	_, initial, err := reader.CodexTurnStateModelPolicy(ctx)
	require.NoError(t, err)
	var changed atomic.Int32
	reader.AddCodexTurnStateModelsListener(func() { changed.Add(1) })
	_, err = writer.persistSettingsAndRefreshOpenAIPolicies(ctx, map[string]string{SettingKeyCodexTurnStateModels: "[]"}, nil)
	require.NoError(t, err)
	models, disabled, err := reader.CodexTurnStateModelPolicyAuthoritative(ctx)
	require.NoError(t, err)
	require.Empty(t, models)
	require.NotEqual(t, initial, disabled)
	require.Equal(t, int32(1), changed.Load())
	_, err = writer.persistSettingsAndRefreshOpenAIPolicies(ctx, map[string]string{SettingKeyCodexTurnStateModels: `["gpt-6-astra","gpt-5.6-sol","gpt-5.6-terra"]`}, nil)
	require.NoError(t, err)
	reader.InvalidateCodexTurnStateModelsCache()
	_, restored, err := reader.CodexTurnStateModelPolicy(ctx)
	require.NoError(t, err)
	require.NotEqual(t, initial, restored, "remove/re-add invalidates frozen old attempts")
	// A direct database change without the server revision still alters the
	// policy fingerprint instead of authorizing old final-model snapshots.
	repo.mu.Lock()
	repo.values[SettingKeyCodexTurnStateModels] = `["custom-model"]`
	repo.mu.Unlock()
	_, directEdit, err := reader.CodexTurnStateModelPolicyAuthoritative(ctx)
	require.NoError(t, err)
	require.NotEqual(t, restored, directEdit)
}

func TestCodexTurnStateModelsPersistenceAndOmittedProtection(t *testing.T) {
	ctx := context.Background()
	repo := newCodexModelsSettingsRepo(map[string]string{SettingKeyCodexTurnStateModels: `["custom"]`, SettingKeyCodexTurnStateModelsRevision: "initial"})
	svc := NewSettingService(repo, &config.Config{})
	_, initial, err := svc.CodexTurnStateModelPolicy(ctx)
	require.NoError(t, err)
	var changes atomic.Int32
	svc.AddCodexTurnStateModelsListener(func() { changes.Add(1) })
	repo.writeErr = errors.New("write failed")
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(ctx, map[string]string{SettingKeyCodexTurnStateModels: "[]"}, nil)
	require.Error(t, err)
	_, revision, err := svc.CodexTurnStateModelPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, initial, revision)
	require.Zero(t, changes.Load())
	repo.writeErr = nil
	repo.readbackErr = errors.New("readback failed")
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(ctx, map[string]string{SettingKeyCodexTurnStateModels: "[]"}, OmittedSettingKeys{SettingKeySiteName: {}})
	require.NoError(t, err)
	models, revision, err := svc.CodexTurnStateModelPolicy(ctx)
	require.NoError(t, err)
	require.Empty(t, models)
	require.NotEqual(t, initial, revision)
	require.Equal(t, int32(1), changes.Load())
	require.NotEmpty(t, repo.writes[0][SettingKeyCodexTurnStateModelsRevision])
	require.Equal(t, "[]", repo.writes[0][SettingKeyCodexTurnStateModels])
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(ctx, map[string]string{SettingKeySiteName: "new name"}, OmittedSettingKeys{SettingKeyCodexTurnStateModels: {}})
	require.NoError(t, err)
	require.NotContains(t, repo.writes[1], SettingKeyCodexTurnStateModels)
	require.NotContains(t, repo.writes[1], SettingKeyCodexTurnStateModelsRevision)
	require.Equal(t, int32(1), changes.Load(), "unrelated saves must not republish a stale policy")
}

func TestCodexTurnStateModelsBuildSettingsDistinguishesNilAndEmpty(t *testing.T) {
	svc := NewSettingService(newCodexModelsSettingsRepo(nil), &config.Config{})
	updates, err := svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{})
	require.NoError(t, err)
	require.NotContains(t, updates, SettingKeyCodexTurnStateModels, "old programmatic settings callers cannot clear the new list")
	updates, err = svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{CodexTurnStateModels: []string{}})
	require.NoError(t, err)
	require.Equal(t, "[]", updates[SettingKeyCodexTurnStateModels])
	_, err = svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{CodexTurnStateModels: []string{"*"}})
	require.Error(t, err)
}
