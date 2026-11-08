package admin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type quotaAssignGroupRepo struct{ service.GroupRepository }

func (quotaAssignGroupRepo) GetByID(context.Context, int64) (*service.Group, error) {
	return &service.Group{ID: 1, SubscriptionType: service.SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true}, nil
}

type quotaAssignSubscriptionRepo struct {
	service.UserSubscriptionRepository
	byUser map[int64]*service.UserSubscription
	events int
}

func (r *quotaAssignSubscriptionRepo) GetByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	if sub := r.byUser[userID]; sub != nil {
		copy := *sub
		return &copy, nil
	}
	return nil, service.ErrSubscriptionNotFound
}
func (r *quotaAssignSubscriptionRepo) Create(_ context.Context, sub *service.UserSubscription) error {
	sub.ID = sub.UserID
	copy := *sub
	r.byUser[sub.UserID] = &copy
	return nil
}
func (r *quotaAssignSubscriptionRepo) CreateQuotaEvent(_ context.Context, event *service.UserSubscriptionQuotaEvent) error {
	r.events++
	event.ID = int64(r.events)
	r.byUser[event.UserSubscriptionID].TotalRemainingUSD += event.QuotaTotalUSD
	return nil
}
func (r *quotaAssignSubscriptionRepo) RetireDepletedQuotaEventsOnAppend(context.Context, int64, int64, time.Time) error {
	return nil
}
func (r *quotaAssignSubscriptionRepo) GetByID(_ context.Context, id int64) (*service.UserSubscription, error) {
	copy := *r.byUser[id]
	return &copy, nil
}
func (r *quotaAssignSubscriptionRepo) GetByIDForUpdate(ctx context.Context, id int64) (*service.UserSubscription, error) {
	return r.GetByID(ctx, id)
}

func TestSubscriptionAssignQuotaIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, path, body string
		expected         int
	}{
		{"single", "/api/v1/admin/subscriptions/assign", `{"user_id":1,"group_id":1,"quota_amount":12.5}`, 1},
		{"bulk", "/api/v1/admin/subscriptions/bulk-assign", `{"user_ids":[1,2,1],"group_id":1,"quota_amount":12.5}`, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
			t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
			repo := &quotaAssignSubscriptionRepo{byUser: map[int64]*service.UserSubscription{}}
			svc := service.NewSubscriptionService(quotaAssignGroupRepo{}, repo, nil, nil, nil)
			t.Cleanup(svc.Stop)
			handler := NewSubscriptionHandler(svc)
			router := gin.New()
			if test.name == "single" {
				router.POST(test.path, handler.Assign)
			} else {
				router.POST(test.path, handler.BulkAssign)
			}
			first := bulkActionHandlerRequest(router, test.path, test.body, "quota-grant-1")
			require.Equal(t, 200, first.Code, first.Body.String())
			replayed := bulkActionHandlerRequest(router, test.path, test.body, "quota-grant-1")
			require.Equal(t, 200, replayed.Code, replayed.Body.String())
			require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
			require.JSONEq(t, first.Body.String(), replayed.Body.String())
			require.Equal(t, test.expected, repo.events)
			second := bulkActionHandlerRequest(router, test.path, test.body, "quota-grant-2")
			require.Equal(t, 200, second.Code, second.Body.String())
			require.Equal(t, 2*test.expected, repo.events)
			for _, sub := range repo.byUser {
				require.Equal(t, 25.0, sub.TotalRemainingUSD)
				require.Nil(t, sub.ExpiresAt)
			}
		})
	}
}

type quotaAssignContextAwareIdempotencyRepo struct {
	*memoryIdempotencyRepoStub
	savedWithLiveBoundedContext bool
}

func (r *quotaAssignContextAwareIdempotencyRepo) checkPersistenceContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, bounded := ctx.Deadline()
	r.savedWithLiveBoundedContext = bounded && time.Until(deadline) <= 5*time.Second
	return nil
}

func (r *quotaAssignContextAwareIdempotencyRepo) MarkSucceeded(ctx context.Context, id int64, status int, body string, expiresAt time.Time) error {
	if err := r.checkPersistenceContext(ctx); err != nil {
		return err
	}
	return r.memoryIdempotencyRepoStub.MarkSucceeded(ctx, id, status, body, expiresAt)
}

func (r *quotaAssignContextAwareIdempotencyRepo) MarkFailedRetryable(ctx context.Context, id int64, reason string, lockedUntil, expiresAt time.Time) error {
	if err := r.checkPersistenceContext(ctx); err != nil {
		return err
	}
	return r.memoryIdempotencyRepoStub.MarkFailedRetryable(ctx, id, reason, lockedUntil, expiresAt)
}

type quotaAssignPostCommitCache struct {
	service.BillingCache
	afterGrant func()
}

func (c *quotaAssignPostCommitCache) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	c.afterGrant()
	return nil
}

func TestSubscriptionAssignQuotaDisconnectAfterGrantReplaysSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	idempotencyRepo := &quotaAssignContextAwareIdempotencyRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub()}
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(idempotencyRepo, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	repo := &quotaAssignSubscriptionRepo{byUser: map[int64]*service.UserSubscription{}}
	requestCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	cache := &quotaAssignPostCommitCache{afterGrant: func() {
		// Assignment invalidates caches only after its grant transaction returns.
		require.Equal(t, 1, repo.events)
		require.Equal(t, 12.5, repo.byUser[1].TotalRemainingUSD)
		disconnect()
	}}
	billing := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	svc := service.NewSubscriptionService(quotaAssignGroupRepo{}, repo, billing, nil, nil)
	t.Cleanup(svc.Stop)
	router := gin.New()
	router.POST("/api/v1/admin/subscriptions/assign", NewSubscriptionHandler(svc).Assign)
	const path = "/api/v1/admin/subscriptions/assign"
	const body = `{"user_id":1,"group_id":1,"quota_amount":12.5}`
	const key = "quota-grant-disconnect"
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)).WithContext(requestCtx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	first := httptest.NewRecorder()
	router.ServeHTTP(first, req)
	require.ErrorIs(t, requestCtx.Err(), context.Canceled)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.True(t, idempotencyRepo.savedWithLiveBoundedContext)
	stored, err := idempotencyRepo.GetByScopeAndKeyHash(context.Background(), "admin.subscriptions.assign", service.HashIdempotencyKey(key))
	require.NoError(t, err)
	require.Equal(t, service.IdempotencyStatusSucceeded, stored.Status)
	require.Nil(t, stored.LockedUntil)
	for range 2 {
		replayed := bulkActionHandlerRequest(router, path, body, key)
		require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
		require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
		require.JSONEq(t, first.Body.String(), replayed.Body.String())
	}
	require.Equal(t, 1, repo.events)
	require.Equal(t, 12.5, repo.byUser[1].TotalRemainingUSD)
}

type quotaAssignFailingGroupRepo struct {
	quotaAssignGroupRepo
	failOnce func() error
}

func (r *quotaAssignFailingGroupRepo) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	if fail := r.failOnce; fail != nil {
		r.failOnce = nil
		return nil, fail()
	}
	return r.quotaAssignGroupRepo.GetByID(ctx, id)
}

func TestSubscriptionAssignQuotaFailureAfterDisconnectRemainsRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	idempotencyRepo := &quotaAssignContextAwareIdempotencyRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub()}
	cfg := service.DefaultIdempotencyConfig()
	cfg.FailedRetryBackoff = 0
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(idempotencyRepo, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	requestCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	groupRepo := &quotaAssignFailingGroupRepo{failOnce: func() error {
		disconnect()
		return errors.New("injected group lookup failure")
	}}
	repo := &quotaAssignSubscriptionRepo{byUser: map[int64]*service.UserSubscription{}}
	svc := service.NewSubscriptionService(groupRepo, repo, nil, nil, nil)
	t.Cleanup(svc.Stop)
	router := gin.New()
	const path = "/api/v1/admin/subscriptions/assign"
	const body = `{"user_id":1,"group_id":1,"quota_amount":12.5}`
	const key = "quota-grant-retry-after-failure"
	router.POST(path, NewSubscriptionHandler(svc).Assign)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)).WithContext(requestCtx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	first := httptest.NewRecorder()
	router.ServeHTTP(first, req)
	require.Equal(t, http.StatusInternalServerError, first.Code, first.Body.String())
	require.Zero(t, repo.events)
	require.True(t, idempotencyRepo.savedWithLiveBoundedContext)
	stored, err := idempotencyRepo.GetByScopeAndKeyHash(context.Background(), "admin.subscriptions.assign", service.HashIdempotencyKey(key))
	require.NoError(t, err)
	require.Equal(t, service.IdempotencyStatusFailedRetryable, stored.Status)
	retry := bulkActionHandlerRequest(router, path, body, key)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	replayed := bulkActionHandlerRequest(router, path, body, key)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, repo.events)
}
