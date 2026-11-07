//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

type failingQuotaEventRepository struct {
	service.UserSubscriptionRepository
}

func (r *failingQuotaEventRepository) CreateQuotaEvent(context.Context, *service.UserSubscriptionQuotaEvent) error {
	return errors.New("injected quota event write failure")
}

func TestTotalQuotaMigrationFreshInstallAndRestart(t *testing.T) {
	before := mergeV028MigrationLedger(t, integrationDB)
	require.Contains(t, before, "279_total_quota_grant_amount_and_permanent.sql")
	for range 2 {
		require.NoError(t, ApplyMigrations(context.Background(), integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
}

func TestTotalQuotaConcurrentGrantsAndRedeemAtomicity(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{})
	g := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("quota-grant-%d", u.ID), SubscriptionType: service.SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true})
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM redeem_codes WHERE group_id=$1`,
			`DELETE FROM user_subscriptions WHERE group_id=$1`,
			`DELETE FROM groups WHERE id=$1`,
		} {
			_, err := integrationDB.Exec(statement, g.ID)
			require.NoError(t, err)
		}
		_, err := integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID)
		require.NoError(t, err)
	})
	r := NewUserSubscriptionRepository(client)
	gr := NewGroupRepository(client, integrationDB)
	subs := service.NewSubscriptionService(gr, r, nil, client, nil)
	amount := 1.23456789
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, _, err := subs.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{UserID: u.ID, GroupID: g.ID, QuotaAmount: &amount})
			results <- err
		}()
	}
	for range 8 {
		require.NoError(t, <-results)
	}
	stored, err := r.GetByUserIDAndGroupID(ctx, u.ID, g.ID)
	require.NoError(t, err)
	require.Nil(t, stored.ExpiresAt)
	summary, err := r.GetQuotaSummary(ctx, stored.ID, time.Now())
	require.NoError(t, err)
	require.InDelta(t, amount*8, summary.TotalRemainingUSD, 1e-8)
	codes := NewRedeemCodeRepository(client)
	code := &service.RedeemCode{Code: fmt.Sprintf("quota-atomic-%d", u.ID), Type: service.RedeemTypeSubscription, GroupID: &g.ID, QuotaAmount: &amount, Status: service.StatusUnused}
	require.NoError(t, codes.Create(ctx, code))
	failureSubs := service.NewSubscriptionService(gr, &failingQuotaEventRepository{r}, nil, client, nil)
	failureRedeem := service.NewRedeemService(codes, NewUserRepository(client, integrationDB), failureSubs, nil, nil, client, nil, nil)
	_, err = failureRedeem.RedeemForAdminFulfillment(ctx, u.ID, code.Code)
	require.ErrorContains(t, err, "injected quota event write failure")
	unchanged, err := codes.GetByID(ctx, code.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusUnused, unchanged.Status)
	afterFailure, err := r.GetQuotaSummary(ctx, stored.ID, time.Now())
	require.NoError(t, err)
	require.Equal(t, summary.TotalRemainingUSD, afterFailure.TotalRemainingUSD)
	redeem := service.NewRedeemService(codes, NewUserRepository(client, integrationDB), subs, nil, nil, client, nil, nil)
	_, err = redeem.RedeemForAdminFulfillment(ctx, u.ID, code.Code)
	require.NoError(t, err)
	_, err = redeem.RedeemForAdminFulfillment(ctx, u.ID, code.Code)
	require.ErrorIs(t, err, service.ErrRedeemCodeUsed)
	final, err := r.GetQuotaSummary(ctx, stored.ID, time.Now())
	require.NoError(t, err)
	require.InDelta(t, amount*9, final.TotalRemainingUSD, 1e-8)
}

func TestTotalQuotaPermanentRepositoryLifecycle(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	u := mustCreateUser(t, client, &service.User{})
	g := mustCreateGroup(t, client, &service.Group{Name: "permanent-lifecycle", SubscriptionType: service.SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true})
	r := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)
	now := time.Now().UTC()
	sub := &service.UserSubscription{UserID: u.ID, GroupID: g.ID, StartsAt: now.Add(-time.Hour), Status: service.SubscriptionStatusActive}
	require.NoError(t, r.LockQuotaAssignment(ctx, u.ID, g.ID))
	require.NoError(t, r.Create(ctx, sub))
	permanent := &service.UserSubscriptionQuotaEvent{UserSubscriptionID: sub.ID, QuotaTotalUSD: 20.12345678, StartsAt: now, SourceKind: "admin"}
	finite := &service.UserSubscriptionQuotaEvent{UserSubscriptionID: sub.ID, QuotaTotalUSD: 10, StartsAt: now, ExpiresAt: quotaExpiryPointer(now.Add(time.Hour)), SourceKind: "admin"}
	require.NoError(t, r.CreateQuotaEvent(ctx, permanent))
	require.NoError(t, r.CreateQuotaEvent(ctx, finite))
	require.NoError(t, r.RetireDepletedQuotaEventsOnAppend(ctx, sub.ID, finite.ID, now))
	stored, err := r.GetByID(ctx, sub.ID)
	require.NoError(t, err)
	require.Nil(t, stored.ExpiresAt)
	require.True(t, stored.Group.TotalQuotaNoExpiry)
	active, err := r.GetActiveByUserIDAndGroupID(ctx, u.ID, g.ID)
	require.NoError(t, err)
	require.Equal(t, sub.ID, active.ID)
	summary, err := r.GetQuotaSummary(ctx, sub.ID, now)
	require.NoError(t, err)
	require.InDelta(t, 30.12345678, summary.TotalRemainingUSD, 1e-8)
	require.WithinDuration(t, *finite.ExpiresAt, *summary.NextQuotaExpireAt, time.Microsecond)
	require.Equal(t, float64(10), summary.NextExpiringQuotaUSD)
	snapshot, err := r.GetQuotaSpendSnapshot(ctx, sub.ID, now)
	require.NoError(t, err)
	require.Equal(t, []int64{finite.ID, permanent.ID}, snapshot.EventIDs)
	require.NoError(t, r.IncrementUsage(ctx, sub.ID, 12))
	finiteStored, err := client.UserSubscriptionQuotaEvent.Get(ctx, finite.ID)
	require.NoError(t, err)
	require.Equal(t, float64(10), finiteStored.QuotaUsedUsd)
	permanentStored, err := client.UserSubscriptionQuotaEvent.Get(ctx, permanent.ID)
	require.NoError(t, err)
	require.Equal(t, float64(2), permanentStored.QuotaUsedUsd)
	require.Nil(t, permanentStored.ExpiresAt)
	_, err = r.BatchUpdateExpiredStatus(ctx)
	require.NoError(t, err)
	stored, err = r.GetByID(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, stored.Status)
	summary, err = r.GetQuotaSummary(ctx, sub.ID, now.Add(24*time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 18.12345678, summary.TotalRemainingUSD, 1e-8)
	require.Nil(t, summary.NextQuotaExpireAt)
	// Exhausting a permanent grant must not manufacture an expiry date: the
	// historical grant stays permanent when a later grant is appended.
	_, err = client.UserSubscriptionQuotaEvent.UpdateOneID(permanent.ID).SetQuotaUsedUsd(permanent.QuotaTotalUSD).Save(ctx)
	require.NoError(t, err)
	next := &service.UserSubscriptionQuotaEvent{UserSubscriptionID: sub.ID, QuotaTotalUSD: 5, StartsAt: now, SourceKind: "admin"}
	require.NoError(t, r.CreateQuotaEvent(ctx, next))
	require.NoError(t, r.RetireDepletedQuotaEventsOnAppend(ctx, sub.ID, next.ID, now))
	permanentStored, err = client.UserSubscriptionQuotaEvent.Get(ctx, permanent.ID)
	require.NoError(t, err)
	require.Nil(t, permanentStored.ExpiresAt)
}

func TestTotalQuotaGroupModeAndRedeemSnapshot(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	g := mustCreateGroup(t, client, &service.Group{Name: "permanent-mode", SubscriptionType: service.SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true})
	gr := NewGroupRepository(client, integrationDB)
	got, err := gr.GetByID(ctx, g.ID)
	require.NoError(t, err)
	got.TotalQuotaNoExpiry = false // A stale whole-group snapshot cannot reset the mode.
	require.NoError(t, gr.Update(ctx, got))
	got, err = gr.GetByID(ctx, g.ID)
	require.NoError(t, err)
	require.True(t, got.TotalQuotaNoExpiry)
	got.SubscriptionType = service.SubscriptionTypeStandard
	require.ErrorContains(t, gr.Update(ctx, got), "permanent quota groups cannot change billing type")
	r := NewRedeemCodeRepository(client).(*redeemCodeRepository)
	amount := 12.12345678
	code := &service.RedeemCode{Code: fmt.Sprintf("quota-snapshot-%d", g.ID), Type: service.RedeemTypeSubscription, Status: service.StatusDisabled, GroupID: &g.ID, QuotaAmount: &amount, ValidityDays: 30}
	require.NoError(t, r.Create(ctx, code))
	status := service.StatusUnused
	_, err = r.BatchUpdate(ctx, []int64{code.ID}, service.RedeemCodeBatchUpdateFields{Status: &status})
	require.NoError(t, err)
	loaded, err := r.GetByID(ctx, code.ID)
	require.NoError(t, err)
	require.Equal(t, 0, loaded.ValidityDays)
	require.NotNil(t, loaded.QuotaAmount)
	require.InDelta(t, amount, *loaded.QuotaAmount, 1e-8)
	legacy := &service.RedeemCode{Code: fmt.Sprintf("legacy-snapshot-%d", g.ID), Type: service.RedeemTypeSubscription, Status: service.StatusDisabled, GroupID: &g.ID, Value: 100, ValidityDays: 30}
	require.NoError(t, r.Create(ctx, legacy))
	_, err = r.BatchUpdate(ctx, []int64{legacy.ID}, service.RedeemCodeBatchUpdateFields{Status: &status})
	require.Error(t, err)
	loaded, err = r.GetByID(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusDisabled, loaded.Status)
}

func TestTotalQuotaMigrationPreservesGrantsAndOrdinaryDefaults(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	var quotaGroup, normalGroup, userID, subscriptionID int64
	require.NoError(t, tx.QueryRow(`INSERT INTO groups(name, subscription_type, total_limit_usd) VALUES('quota-migration','total_quota',90) RETURNING id`).Scan(&quotaGroup))
	require.NoError(t, tx.QueryRow(`INSERT INTO groups(name, subscription_type) VALUES('normal-migration','subscription') RETURNING id`).Scan(&normalGroup))
	require.NoError(t, tx.QueryRow(`INSERT INTO users(email,password_hash) VALUES($1,'test') RETURNING id`, fmt.Sprintf("quota-migration-%d@test.invalid", quotaGroup)).Scan(&userID))
	require.NoError(t, tx.QueryRow(`INSERT INTO user_subscriptions(user_id,group_id,starts_at,expires_at,status,assigned_at) VALUES($1,$2,NOW(),NOW()+INTERVAL '30 days','active',NOW()) RETURNING id`, userID, quotaGroup).Scan(&subscriptionID))
	_, err := tx.Exec(`INSERT INTO user_subscription_quota_events(user_subscription_id,quota_total_usd,quota_used_usd,starts_at,expires_at,source_kind) VALUES($1,90,17,NOW(),NOW()+INTERVAL '30 days','admin')`, subscriptionID)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO redeem_codes(code,type,value,status,group_id,used_by,used_at,quota_amount) VALUES
		($1,'subscription',10,'unused',$4,NULL,NULL,NULL),
		($2,'subscription',10,'used',$4,$5,NOW(),NULL),
		($3,'subscription',0,'unused',$4,NULL,NULL,42)`, fmt.Sprintf("old-%d", quotaGroup), fmt.Sprintf("used-%d", quotaGroup), fmt.Sprintf("new-%d", quotaGroup), quotaGroup, userID)
	require.NoError(t, err)
	defaults := fmt.Sprintf(`[{"group_id":%d,"validity_days":30},{"group_id":%d,"validity_days":7}]`, quotaGroup, normalGroup)
	for _, key := range []string{"default_subscriptions", "auth_source_default_email_subscriptions", "auth_source_default_oidc_subscriptions"} {
		_, err = tx.Exec(`INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, key, defaults)
		require.NoError(t, err)
	}
	var preserved string
	require.NoError(t, tx.QueryRow(`SELECT row_to_json(e)::text FROM user_subscription_quota_events e WHERE user_subscription_id=$1`, subscriptionID).Scan(&preserved))
	ddl, err := dbmigrations.FS.ReadFile("279_total_quota_grant_amount_and_permanent.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(ddl))
		require.NoError(t, err)
	}
	for code, expected := range map[string]string{fmt.Sprintf("old-%d", quotaGroup): service.StatusDisabled, fmt.Sprintf("used-%d", quotaGroup): service.StatusUsed, fmt.Sprintf("new-%d", quotaGroup): service.StatusUnused} {
		var status string
		require.NoError(t, tx.QueryRow(`SELECT status FROM redeem_codes WHERE code=$1`, code).Scan(&status))
		require.Equal(t, expected, status)
	}
	for _, key := range []string{"default_subscriptions", "auth_source_default_email_subscriptions", "auth_source_default_oidc_subscriptions"} {
		var value string
		require.NoError(t, tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, key).Scan(&value))
		require.JSONEq(t, fmt.Sprintf(`[{"group_id":%d,"validity_days":7}]`, normalGroup), value)
	}
	var after string
	require.NoError(t, tx.QueryRow(`SELECT row_to_json(e)::text FROM user_subscription_quota_events e WHERE user_subscription_id=$1`, subscriptionID).Scan(&after))
	require.Equal(t, preserved, after)
}
