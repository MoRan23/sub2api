package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIUUIDv7RuntimeRepo struct {
	mu           sync.Mutex
	values       map[string]string
	err          error
	getCalls     atomic.Int32
	firstStarted chan struct{}
	firstRelease chan struct{}
}

func (r *openAIUUIDv7RuntimeRepo) Get(context.Context, string) (*Setting, error) {
	return nil, errors.New("unexpected Get call")
}

func (r *openAIUUIDv7RuntimeRepo) GetValue(_ context.Context, key string) (string, error) {
	return "", errors.New("unexpected GetValue call")
}

func (r *openAIUUIDv7RuntimeRepo) Set(context.Context, string, string) error {
	return errors.New("unexpected Set call")
}

func (r *openAIUUIDv7RuntimeRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	call := r.getCalls.Add(1)
	r.mu.Lock()
	repoErr := r.err
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	r.mu.Unlock()
	if call == 1 && r.firstStarted != nil {
		close(r.firstStarted)
		<-r.firstRelease
	}
	if repoErr != nil {
		return nil, repoErr
	}
	return result, nil
}

func (r *openAIUUIDv7RuntimeRepo) SetMultiple(context.Context, map[string]string) error {
	return errors.New("unexpected SetMultiple call")
}

func (r *openAIUUIDv7RuntimeRepo) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("unexpected GetAll call")
}

func (r *openAIUUIDv7RuntimeRepo) Delete(context.Context, string) error {
	return errors.New("unexpected Delete call")
}

func TestIsOpenAIUUIDv7SessionIdentityEnabledCachesAndInvalidates(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
	}}
	svc := NewSettingService(repo, nil)

	require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(1), repo.getCalls.Load(), "successful reads should be cached")

	repo.mu.Lock()
	repo.values[SettingKeyEnableOpenAIUUIDv7SessionIdentity] = "false"
	repo.mu.Unlock()
	// The cached value remains until the settings write path invalidates it.
	require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	svc.InvalidateOpenAIUUIDv7SessionIdentityCache()
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(2), repo.getCalls.Load())
}

func TestIsOpenAIUUIDv7SessionIdentityEnabledUsesCompiledDefaultBeforeFirstSuccess(t *testing.T) {
	for name, repoErr := range map[string]error{
		"missing":        ErrSettingNotFound,
		"database error": errors.New("database unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			repo := &openAIUUIDv7RuntimeRepo{err: repoErr}
			svc := NewSettingService(repo, nil)
			require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
			require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
			require.Equal(t, int32(1), repo.getCalls.Load(), "failed reads should still be briefly cached")
		})
	}
}

func TestIsOpenAIUUIDv7SessionIdentityEnabledMalformedValueUsesCompiledDefault(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: " true ",
	}}
	svc := NewSettingService(repo, nil)

	require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(1), repo.getCalls.Load())
}

func TestIsOpenAIUUIDv7SessionIdentityEnabledUsesLastKnownGoodOnReadFailure(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "false",
	}}
	svc := NewSettingService(repo, nil)
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))

	repo.mu.Lock()
	repo.err = errors.New("database unavailable")
	repo.mu.Unlock()
	svc.InvalidateOpenAIUUIDv7SessionIdentityCache()

	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
}

func TestPublishOpenAIUUIDv7SessionIdentityTakesEffectWithoutDatabaseRead(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{err: errors.New("database unavailable")}
	svc := NewSettingService(repo, nil)

	svc.PublishOpenAIUUIDv7SessionIdentity(false)
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(0), repo.getCalls.Load())
}

func TestPublishOpenAIUUIDv7SessionIdentityWinsStaleReadCommitRace(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
	}}
	svc := NewSettingService(repo, nil)
	commitReady := make(chan struct{})
	commitRelease := make(chan struct{})
	var pauseOnce sync.Once
	svc.openAIUUIDv7SessionIdentityBeforeCommit = func() {
		pauseOnce.Do(func() {
			close(commitReady)
			<-commitRelease
		})
	}

	staleResult := make(chan bool, 1)
	go func() {
		staleResult <- svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background())
	}()
	<-commitReady

	// The admin publish lands after the stale read's optimistic generation
	// check, but before its locked commit. A subsequent DB failure must not let
	// that stale true value replace the explicitly published false rollback.
	svc.PublishOpenAIUUIDv7SessionIdentity(false)
	repo.mu.Lock()
	repo.err = errors.New("database unavailable")
	repo.mu.Unlock()
	close(commitRelease)

	require.False(t, <-staleResult)
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(1), repo.getCalls.Load())
}

func TestPublishOpenAIUUIDv7SessionIdentityPublishesSnapshotBeforeGeneration(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{
		values: map[string]string{
			SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
		},
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
	svc := NewSettingService(repo, nil)
	cacheStored := make(chan struct{})
	generationRelease := make(chan struct{})
	svc.openAIUUIDv7SessionIdentityBeforeGenerationStore = func() {
		close(cacheStored)
		<-generationRelease
	}

	publishDone := make(chan struct{})
	go func() {
		svc.PublishOpenAIUUIDv7SessionIdentity(false)
		close(publishDone)
	}()
	<-cacheStored

	// While Publish is between its cache and generation stores, a reader sees
	// the complete next-generation cache. It may start an old-generation DB
	// read, but that read must not commit after Publish advances the generation.
	readResult := make(chan bool, 1)
	go func() {
		readResult <- svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background())
	}()
	<-repo.firstStarted
	close(generationRelease)
	<-publishDone
	close(repo.firstRelease)

	require.False(t, <-readResult)
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(1), repo.getCalls.Load())
}

func TestIsOpenAIUUIDv7SessionIdentityEnabledAcceptsNilContext(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
	}}
	svc := NewSettingService(repo, nil)
	require.True(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(nil))
}

func TestGetOpenAICodexFingerprintPolicyLoadsOneAtomicGeneration(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization:    "true",
		SettingKeyEnableOpenAICodexInstallationIDNormalization: "false",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:            "true",
		SettingKeyEnableOpenAICodexClientIdentityNormalization: "false",
	}}
	svc := NewSettingService(repo, nil)

	policy := svc.GetOpenAICodexFingerprintPolicy(context.Background())
	require.True(t, policy.MasterEnabled)
	require.False(t, policy.InstallationIDEnabled)
	require.True(t, policy.TurnIdentityEnabled)
	require.False(t, policy.ClientIdentityEnabled)
	require.False(t, policy.InstallationIDNormalizationEnabled())
	require.True(t, policy.TurnIdentityNormalizationEnabled())
	require.False(t, policy.ClientIdentityNormalizationEnabled())
	require.Equal(t, int32(1), repo.getCalls.Load())
}

func TestGetOpenAICodexFingerprintPolicyMasterDisablesEveryEffectiveChild(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization:    "false",
		SettingKeyEnableOpenAICodexInstallationIDNormalization: "true",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:            "true",
		SettingKeyEnableOpenAICodexClientIdentityNormalization: "true",
	}}
	svc := NewSettingService(repo, nil)
	policy := svc.GetOpenAICodexFingerprintPolicy(context.Background())

	require.False(t, policy.InstallationIDNormalizationEnabled())
	require.False(t, policy.TurnIdentityNormalizationEnabled())
	require.False(t, policy.ClientIdentityNormalizationEnabled())
	require.True(t, policy.InstallationIDEnabled, "child preferences remain available for preconfiguration")
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
}

func TestGetOpenAICodexFingerprintPolicyMalformedFieldUsesWholeLastKnownGoodSnapshot(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization:    "false",
		SettingKeyEnableOpenAICodexInstallationIDNormalization: "false",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:            "false",
		SettingKeyEnableOpenAICodexClientIdentityNormalization: "false",
	}}
	svc := NewSettingService(repo, nil)
	want := svc.GetOpenAICodexFingerprintPolicy(context.Background())

	repo.mu.Lock()
	repo.values[SettingKeyEnableOpenAICodexFingerprintNormalization] = "true"
	repo.values[SettingKeyEnableOpenAICodexInstallationIDNormalization] = "malformed"
	repo.values[SettingKeyEnableOpenAIUUIDv7SessionIdentity] = "true"
	repo.values[SettingKeyEnableOpenAICodexClientIdentityNormalization] = "true"
	repo.mu.Unlock()
	svc.InvalidateOpenAIUUIDv7SessionIdentityCache()

	got := svc.GetOpenAICodexFingerprintPolicy(context.Background())
	require.Equal(t, want.MasterEnabled, got.MasterEnabled)
	require.Equal(t, want.InstallationIDEnabled, got.InstallationIDEnabled)
	require.Equal(t, want.TurnIdentityEnabled, got.TurnIdentityEnabled)
	require.Equal(t, want.ClientIdentityEnabled, got.ClientIdentityEnabled)
	require.Greater(t, got.Generation, want.Generation)
}

func TestOpenAIUUIDv7SessionIdentityInvalidationRejectsLateStaleRead(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{
		values: map[string]string{
			SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
		},
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
	svc := NewSettingService(repo, nil)
	staleResult := make(chan bool, 1)
	go func() {
		staleResult <- svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background())
	}()
	<-repo.firstStarted

	// Model the settings write ordering: persist the new value, then invalidate.
	repo.mu.Lock()
	repo.values[SettingKeyEnableOpenAIUUIDv7SessionIdentity] = "false"
	repo.mu.Unlock()
	svc.InvalidateOpenAIUUIDv7SessionIdentityCache()

	// A post-invalidation caller must not join the old generation's blocked
	// singleflight read and must observe the newly persisted value.
	freshResult := make(chan bool, 1)
	go func() {
		freshResult <- svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background())
	}()
	require.False(t, <-freshResult)
	close(repo.firstRelease)
	require.False(t, <-staleResult, "the late old-generation read must retry instead of returning stale true")
	require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
	require.Equal(t, int32(2), repo.getCalls.Load())
}
