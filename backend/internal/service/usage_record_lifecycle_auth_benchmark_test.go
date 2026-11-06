package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// BenchmarkUsageRecordLifecycleAuthenticated keeps the original lifecycle
// subjects and worker work, and adds the production GetByKey L1-hit path to each
// timed request in both modes. Publication and its independent billing projection
// happen through a real cache miss before timing; no fixture marks a Group frozen.
// The original BenchmarkUsageRecordLifecycle still exercises untrusted subjects
// and the deferred-billing deep-copy fallback.
func BenchmarkUsageRecordLifecycleAuthenticated(b *testing.B) {
	for _, kind := range []string{"ws_replay", "systemone_response", "grok_request"} {
		for _, payloadBytes := range []int{8 << 10, 1 << 20} {
			for _, frozen := range []bool{false, true} {
				ownership := "legacy_capture"
				if frozen {
					ownership = "owned_snapshot"
				}
				b.Run(fmt.Sprintf("%s/bytes=%d/%s", kind, payloadBytes, ownership), func(b *testing.B) {
					benchmarkUsageRecordLifecycleWithAuthentication(b, kind, payloadBytes, frozen, true)
				})
			}
		}
	}
}

type usageLifecycleAuthRepository struct {
	APIKeyRepository
	key     *APIKey
	lookups atomic.Int64
}

func (r *usageLifecycleAuthRepository) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	r.lookups.Add(1)
	return r.key, nil
}

func newUsageLifecycleAuthentication(b *testing.B, key *APIKey) *APIKeyService {
	b.Helper()
	repo := &usageLifecycleAuthRepository{key: key}
	svc := &APIKeyService{apiKeyRepo: repo}
	svc.initAuthCache(&config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{
		L1Size: 128, L1TTLSeconds: 3600, Singleflight: true,
	}})
	if svc.authCacheL1 == nil {
		b.Fatal("authentication L1 cache was not initialized")
	}
	b.Cleanup(func() {
		svc.authCacheL1.Close()
		if got := repo.lookups.Load(); got != 1 {
			b.Errorf("authenticated warm benchmark performed %d repository reads; want 1", got)
		}
	})
	if _, err := svc.GetByKey(context.Background(), key.Key); err != nil {
		b.Fatalf("publish authentication cache: %v", err)
	}
	svc.authCacheL1.Wait()
	if _, ok := svc.authCacheL1.Get(svc.authCacheKey(key.Key)); !ok {
		b.Fatal("authentication cache publication was not retained")
	}
	return svc
}
