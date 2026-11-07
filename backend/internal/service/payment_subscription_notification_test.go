package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

type paymentNotificationSubscriptionRepo struct {
	UserSubscriptionRepository
	subscription *UserSubscription
}

func (r *paymentNotificationSubscriptionRepo) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	return r.subscription, nil
}

func TestPaymentSubscriptionNotificationHandlesNullableExpiry(t *testing.T) {
	expiresAt := time.Date(2030, time.January, 2, 3, 4, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		expiresAt *time.Time
		wantBody  string
	}{
		{name: "permanent", wantBody: "<p>expiry=</p>"},
		{name: "finite", expiresAt: &expiresAt, wantBody: "<p>expiry=2030-01-02 03:04</p>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			settings := newNotificationEmailMemorySettingRepo()
			smtpServer := startNotificationEmailTestSMTPServer(t)
			require.NoError(t, settings.SetMultiple(ctx, smtpServer.settings()))
			notifications := NewNotificationEmailService(settings, NewEmailService(settings, nil))
			_, err := notifications.UpdateTemplate(ctx, NotificationEmailEventSubscriptionPurchaseSuccess, "en", "Subscription", "<p>expiry={{expiry_time}}</p>")
			require.NoError(t, err)
			svc := &PaymentService{
				notificationEmailService: notifications,
				subscriptionSvc: &SubscriptionService{userSubRepo: &paymentNotificationSubscriptionRepo{
					subscription: &UserSubscription{ExpiresAt: tc.expiresAt},
				}},
			}
			groupID := int64(7)
			order := &dbent.PaymentOrder{ID: 1, UserID: 2, UserEmail: "subscriber@example.com", SubscriptionGroupID: &groupID}

			require.NoError(t, svc.sendSubscriptionPurchaseSuccessNotification(ctx, order))
			require.Equal(t, int64(1), smtpServer.messageCount())
			require.Contains(t, smtpServer.lastMessageBody(t), tc.wantBody)
		})
	}
}
