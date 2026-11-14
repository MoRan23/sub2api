//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type visibilityUserRepo struct {
	UserRepository
	user *User
	err  error
}

func (r *visibilityUserRepo) GetByID(context.Context, int64) (*User, error) { return r.user, r.err }

type visibilitySubRepo struct {
	UserSubscriptionRepository
	subscriptions []UserSubscription
	err           error
	calls         int
}

func (r *visibilitySubRepo) ListActiveByUserID(_ context.Context, userID int64) ([]UserSubscription, error) {
	r.calls++
	// Match the repository's active-status and expiry predicates.
	active := make([]UserSubscription, 0)
	for _, sub := range r.subscriptions {
		if sub.UserID == userID && sub.IsActive() {
			active = append(active, sub)
		}
	}
	return active, r.err
}

type visibilityGroupRepo struct {
	GroupRepository
	groups []Group
}

func (r *visibilityGroupRepo) ListActive(context.Context) ([]Group, error) { return r.groups, nil }

func TestGetUserGroupVisibilityIncludesActiveSubscriptions(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrestricted", true: "restricted"}[restricted], func(t *testing.T) {
			now := time.Now()
			subs := &visibilitySubRepo{subscriptions: []UserSubscription{
				{UserID: 1, GroupID: 42, Status: SubscriptionStatusActive, ExpiresAt: new(now.Add(time.Hour))},
				{UserID: 1, GroupID: 43, Status: SubscriptionStatusActive, ExpiresAt: new(now.Add(-time.Hour))},
				{UserID: 1, GroupID: 44, Status: "expired", ExpiresAt: new(now.Add(time.Hour))},
				{UserID: 2, GroupID: 45, Status: SubscriptionStatusActive, ExpiresAt: new(now.Add(time.Hour))},
			}}
			svc := &APIKeyService{
				userRepo:    &visibilityUserRepo{user: &User{ID: 1, AllowedGroups: []int64{7}, RestrictPublicGroups: restricted}},
				userSubRepo: subs,
				groupRepo:   &visibilityGroupRepo{groups: []Group{{ID: 42, IsExclusive: true, SubscriptionType: "subscription"}}},
			}
			available, err := svc.GetAvailableGroups(context.Background(), 1)
			require.NoError(t, err)
			require.Len(t, available, 1)
			visible, restrict, err := svc.GetUserGroupVisibility(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, restricted, restrict)
			require.Equal(t, map[int64]struct{}{7: {}, 42: {}}, visible)
			require.Contains(t, visible, available[0].ID, "a subscribed group that can be bound must be visible")
		})
	}
}

func TestGetUserGroupVisibilityEmptyAndErrors(t *testing.T) {
	failure := errors.New("repository unavailable")
	for _, tc := range []struct {
		name            string
		userErr, subErr error
	}{
		{name: "empty"}, {name: "user failure", userErr: failure}, {name: "subscription failure", subErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subs := &visibilitySubRepo{err: tc.subErr}
			svc := &APIKeyService{userRepo: &visibilityUserRepo{user: &User{ID: 1}, err: tc.userErr}, userSubRepo: subs}
			got, _, err := svc.GetUserGroupVisibility(context.Background(), 1)
			if tc.userErr != nil || tc.subErr != nil {
				require.ErrorIs(t, err, failure)
				require.Nil(t, got, "repository failures must not become anonymous visibility")
			} else {
				require.NoError(t, err)
				require.NotNil(t, got, "an empty logged-in user must not become anonymous")
				require.Empty(t, got)
			}
			if tc.userErr != nil {
				require.Zero(t, subs.calls)
			}
		})
	}
}

func TestGetAvailableGroupsKeepsExhaustedTotalQuotaVisible(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "finite", true: "permanent_null_expiry"}[permanent], func(t *testing.T) {
			group := Group{ID: 42, Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: permanent}
			sub := UserSubscription{UserID: 1, GroupID: group.ID, Status: SubscriptionStatusActive, TotalRemainingUSD: 0}
			if !permanent {
				expires := time.Now().Add(time.Hour)
				sub.ExpiresAt = &expires
			}
			svc := &APIKeyService{
				userRepo:    &visibilityUserRepo{user: &User{ID: 1, Balance: 0, RestrictPublicGroups: true}},
				userSubRepo: &visibilitySubRepo{subscriptions: []UserSubscription{sub}},
				groupRepo:   &visibilityGroupRepo{groups: []Group{group}},
			}
			available, err := svc.GetAvailableGroups(context.Background(), 1)
			require.NoError(t, err)
			require.Len(t, available, 1, "quota and wallet exhaustion must not revoke monitoring visibility")
			require.Equal(t, group.ID, available[0].ID)
			// Revocation is effective on the next read, even though the group stays active.
			svc.userSubRepo.(*visibilitySubRepo).subscriptions[0].Status = SubscriptionStatusExpired
			available, err = svc.GetAvailableGroups(context.Background(), 1)
			require.NoError(t, err)
			require.Empty(t, available)
		})
	}
}
