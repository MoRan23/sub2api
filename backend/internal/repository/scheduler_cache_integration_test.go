//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexWebSearchProviderMetadataPostgresRedisRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := &service.Account{
		Name: "codex-search-provider", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Concurrency: 3, Priority: 50, Schedulable: true,
	}
	metadata := service.UpstreamModelMetadataSnapshot{Models: make(map[string]service.UpstreamModelMetadata)}
	for model, value := range map[string]string{
		"absent": "", "null": "null", "standalone": `"standalone"`, "search": `"search_mcp"`, "future": `"future_provider"`,
	} {
		capabilities := make(map[string]json.RawMessage)
		if value != "" {
			capabilities["web_search_provider"] = json.RawMessage(value)
		}
		metadata.Models[model] = service.UpstreamModelMetadata{ID: model, CodexToolCapabilities: capabilities}
	}
	account.SetUpstreamModelMetadataSnapshot(metadata)
	require.NoError(t, repo.Create(ctx, account))
	restored, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)
	require.NoError(t, cache.SetAccount(ctx, restored))
	cached, err := NewSchedulerCache(rdb).GetAccount(ctx, account.ID)
	require.NoError(t, err)
	for _, persisted := range []*service.Account{restored, cached} {
		require.NotNil(t, persisted)
		for model, expected := range metadata.Models {
			actual, ok := persisted.GetUpstreamModelMetadata(model)
			require.True(t, ok, model)
			require.Equal(t, string(expected.CodexToolCapabilities["web_search_provider"]), string(actual.CodexToolCapabilities["web_search_provider"]), model)
		}
	}

	snapshot := &service.PublicCodexCatalogSnapshot{
		Body: []byte(`{
  "models": [{"slug":"catalog-target","web_search_provider":"search_mcp","unknown":null,
    "model_messages":{"content_filter_guidance":"  Keep original guidance.  ","plugins":{"unknown":true},
      "tools":{"indirect_description_prefixes":{"namespaces":{"functions":"prefix"},"mcp_servers":{"search":"  search guidance  "}},"unknown":[1,null]}}}]
}`),
		CheckedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, NewPublicCodexCatalogStore(rdb).Save(ctx, snapshot))
	public, err := NewPublicCodexCatalogStore(rdb).Load(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot, public)
}

func TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)

	bucket := service.SchedulerBucket{GroupID: 2, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	now := time.Now().UTC().Truncate(time.Second)
	limitReset := now.Add(10 * time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	tempUnschedUntil := now.Add(3 * time.Minute)
	windowEnd := now.Add(5 * time.Hour)

	account := service.Account{
		ID:          101,
		Name:        "gemini-heavy",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    7,
		LastUsedAt:  &now,
		Credentials: map[string]any{
			"api_key":       "gemini-api-key",
			"access_token":  "secret-access-token",
			"project_id":    "proj-1",
			"oauth_type":    "ai_studio",
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
			"huge_blob":     strings.Repeat("x", 4096),
		},
		Extra: map[string]any{
			"mixed_scheduling":             true,
			"window_cost_limit":            12.5,
			"window_cost_sticky_reserve":   8.0,
			"max_sessions":                 4,
			"session_idle_timeout_minutes": 11,
			"unused_large_field":           strings.Repeat("y", 4096),
		},
		RateLimitResetAt:       &limitReset,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		SessionWindowStart:     &now,
		SessionWindowEnd:       &windowEnd,
		SessionWindowStatus:    "active",
		GroupIDs:               []int64{bucket.GroupID},
		Groups:                 []*service.Group{{ID: bucket.GroupID, Description: strings.Repeat("unused group configuration", 1024)}},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 101,
				GroupID:   bucket.GroupID,
				Priority:  5,
				Group:     &service.Group{ID: bucket.GroupID, Name: "gemini-group"},
			},
		},
	}

	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)

	got := snapshot[0]
	require.NotNil(t, got)
	require.Empty(t, got.GetCredential("api_key"))
	require.Equal(t, "proj-1", got.GetCredential("project_id"))
	require.Equal(t, "ai_studio", got.GetCredential("oauth_type"))
	require.NotEmpty(t, got.GetModelMapping())
	require.Empty(t, got.GetCredential("access_token"))
	require.Empty(t, got.GetCredential("huge_blob"))
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Equal(t, 12.5, got.GetWindowCostLimit())
	require.Equal(t, 8.0, got.GetWindowCostStickyReserve())
	require.Equal(t, 4, got.GetMaxSessions())
	require.Equal(t, 11, got.GetSessionIdleTimeoutMinutes())
	require.Nil(t, got.Extra["unused_large_field"])
	require.Equal(t, []int64{bucket.GroupID}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 1)
	require.Equal(t, account.ID, got.AccountGroups[0].AccountID)
	require.Equal(t, bucket.GroupID, got.AccountGroups[0].GroupID)
	require.Nil(t, got.AccountGroups[0].Group)

	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "gemini-api-key", full.GetCredential("api_key"))
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
	require.Equal(t, strings.Repeat("x", 4096), full.GetCredential("huge_blob"))
	require.Equal(t, account.GroupIDs, full.GroupIDs)
	require.Empty(t, full.Groups)
	require.Len(t, full.AccountGroups, 1)
	require.Equal(t, account.ID, full.AccountGroups[0].AccountID)
	require.Equal(t, bucket.GroupID, full.AccountGroups[0].GroupID)
	require.Equal(t, 5, full.AccountGroups[0].Priority)
	require.Nil(t, full.AccountGroups[0].Group)
	require.Nil(t, full.AccountGroups[0].Account)
}

func TestSchedulerCacheRetireAndReopenFencesOldEpochIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)
	bucket := service.SchedulerBucket{GroupID: 77, Platform: service.PlatformAntigravity, Mode: service.SchedulerModeForced}
	account := service.Account{ID: 7701, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth}

	oldToken, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, oldToken, []service.Account{account}))
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	require.NoError(t, cache.RetireBucket(ctx, bucket))

	_, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
	_, err = cache.CaptureBucketWriteToken(ctx, bucket)
	require.ErrorIs(t, err, service.ErrSchedulerBucketRetired)
	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, oldToken, []service.Account{account}), service.ErrSchedulerBucketRetired)

	newToken, err := cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.Greater(t, newToken.Epoch, oldToken.Epoch)
	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, oldToken, []service.Account{account}), service.ErrSchedulerBucketWriteFenced)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, newToken, []service.Account{account}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, account.ID, snapshot[0].ID)
}

func TestSchedulerCacheGroupLifecycleLeaseOwnerAndTTLIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)
	const groupID int64 = 78
	const ttl = 500 * time.Millisecond

	first, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, ttl)
	require.NoError(t, err)
	require.True(t, acquired)
	pttl, err := rdb.PTTL(ctx, schedulerGroupLifecycleLockKey(groupID)).Result()
	require.NoError(t, err)
	require.Positive(t, pttl)
	require.LessOrEqual(t, pttl, ttl)

	var second service.SchedulerGroupLifecycleLease
	require.Eventually(t, func() bool {
		var acquireErr error
		second, acquired, acquireErr = cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
		return acquireErr == nil && acquired
	}, 5*time.Second, 20*time.Millisecond)
	require.NotEqual(t, first.OwnerToken, second.OwnerToken)

	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, first), service.ErrSchedulerGroupLifecycleLeaseLost)
	_, acquired, err = cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
	require.NoError(t, err)
	require.False(t, acquired, "a stale release must not delete the successor lease")

	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, second))
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, second), service.ErrSchedulerGroupLifecycleLeaseLost)
	third, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, third.ValidFor(groupID))
	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, third))
}
