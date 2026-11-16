//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelCatalogStoreBoundedAndFenced(t *testing.T) {
	store := NewModelCatalogStore(1024, 2)
	rev := store.Revision()
	store.Put("a", 1, 100, rev)
	store.Put("b", 2, 100, rev)
	_, ok := store.Get("a")
	require.True(t, ok)
	store.Put("c", 3, 100, rev)
	_, ok = store.Get("b")
	require.False(t, ok)
	store.Put("huge", 4, 1024, rev)
	_, ok = store.Get("huge")
	require.False(t, ok)
	require.LessOrEqual(t, store.bytes, int64(1024))
	require.Len(t, store.entries, 2)
	store.Invalidate()
	store.Put("late", 5, 10, rev)
	_, ok = store.Get("late")
	require.False(t, ok)
}
func TestModelCatalogStoreExpiryAndLoadFailure(t *testing.T) {
	store := NewModelCatalogStore(1024, 2)
	store.Put("expired", 1, 10, store.Revision())
	store.entries["expired"].Value.(*modelCatalogEntry).expires = time.Now().Add(-time.Second)
	_, ok := store.Get("expired")
	require.False(t, ok)
	require.Zero(t, store.bytes)
	calls := 0
	for i := 0; i < 2; i++ {
		_, err := store.Load(context.Background(), "bad", func(context.Context) (any, int64, error) { calls++; return nil, 0, fmt.Errorf("failure") })
		require.Error(t, err)
	}
	require.Equal(t, 2, calls)
}
func TestModelCatalogStoreSlowLoadCannotRepublish(t *testing.T) {
	store := NewModelCatalogStore(1024, 2)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = store.Load(context.Background(), "key", func(context.Context) (any, int64, error) { close(started); <-release; return 1, 10, nil })
	}()
	<-started
	store.Invalidate()
	close(release)
	<-done
	_, ok := store.Get("key")
	require.False(t, ok)
}
func TestCompositeOwnershipIndexExactKeysAndAmbiguity(t *testing.T) {
	index, _ := buildCompositeOwnershipIndex([]Account{
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"shared": "x", "prefix-*": "y", "Exact": "x", "empty": " "}}},
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"shared": "a"}}},
	})
	require.True(t, index["shared"].Ambiguous)
	require.False(t, index["shared"].Matched)
	require.True(t, index["shared"].OpenAIGatewayOAuthClaimed)
	require.True(t, index["Exact"].Matched)
	require.False(t, index["exact"].Matched)
	require.False(t, index["prefix-test"].Matched)
	require.NotContains(t, index, "empty")
	before := len(index)
	for i := 0; i < 1000; i++ {
		_ = index[fmt.Sprint(i)]
	}
	require.Len(t, index, before)
}
func TestCatalogProjectionVisibleExpiry(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Second)
	accounts := []Account{{ID: 1, Status: StatusActive, Schedulable: true}, {ID: 2, Status: StatusActive, Schedulable: true, RateLimitResetAt: &future}, {ID: 3, Status: StatusActive, Schedulable: true, RateLimitResetAt: &past}, {ID: 4, Status: StatusActive, Schedulable: true, ExpiresAt: &past, AutoPauseOnExpired: true}}
	visible := visibleCatalogAccounts(accounts, now)
	require.Len(t, visible, 2)
	require.Equal(t, int64(1), visible[0].ID)
	require.Equal(t, int64(3), visible[1].ID)
	require.Len(t, visibleCatalogAccounts(accounts, future.Add(time.Second)), 3)
}
func BenchmarkCompositeOwnershipIndexLookup(b *testing.B) {
	accounts := make([]Account, 2000)
	for i := range accounts {
		accounts[i] = Account{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"model": "target"}}}
	}
	index, _ := buildCompositeOwnershipIndex(accounts)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = index["model"]
	}
}
func BenchmarkModelCatalogStoreHit(b *testing.B) {
	store := NewModelCatalogStore(64<<20, 512)
	store.Put("group:1", make([]byte, 1<<20), 1<<20, store.Revision())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Get("group:1")
	}
}
