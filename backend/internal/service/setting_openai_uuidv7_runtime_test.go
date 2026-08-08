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
	call := r.getCalls.Add(1)
	r.mu.Lock()
	repoErr := r.err
	value, ok := r.values[key]
	r.mu.Unlock()
	if call == 1 && r.firstStarted != nil {
		close(r.firstStarted)
		<-r.firstRelease
	}
	if repoErr != nil {
		return "", repoErr
	}
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *openAIUUIDv7RuntimeRepo) Set(context.Context, string, string) error {
	return errors.New("unexpected Set call")
}

func (r *openAIUUIDv7RuntimeRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("unexpected GetMultiple call")
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

func TestIsOpenAIUUIDv7SessionIdentityEnabledFailsClosed(t *testing.T) {
	for name, repoErr := range map[string]error{
		"missing":        ErrSettingNotFound,
		"database error": errors.New("database unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			repo := &openAIUUIDv7RuntimeRepo{err: repoErr}
			svc := NewSettingService(repo, nil)
			require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
			require.False(t, svc.IsOpenAIUUIDv7SessionIdentityEnabled(context.Background()))
			require.Equal(t, int32(1), repo.getCalls.Load(), "failed reads should still be briefly cached")
		})
	}
}

func TestIsOpenAIUUIDv7SessionIdentityEnabledRejectsMalformedValue(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: " true ",
	}}
	svc := NewSettingService(repo, nil)

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
