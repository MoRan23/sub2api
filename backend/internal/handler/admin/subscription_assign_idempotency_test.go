package admin

import (
	"context"
	"testing"
	"time"

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
