package securityaudit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type notificationStoreStub struct {
	n                            *EnforcementNotification
	current                      bool
	checkErr                     error
	completed, retried, released int
	retryError                   string
	checkedEmail                 string
}

func (s *notificationStoreStub) ClaimEnforcementNotification(context.Context, string, time.Time, time.Duration) (*EnforcementNotification, error) {
	return s.n, nil
}
func (s *notificationStoreStub) EnforcementNotificationCurrent(_ context.Context, _, _ int64, email string) (bool, error) {
	s.checkedEmail = email
	return s.current, s.checkErr
}
func (s *notificationStoreStub) CompleteEnforcementNotification(context.Context, int64, int64, time.Time) error {
	s.completed++
	return nil
}
func (s *notificationStoreStub) RetryEnforcementNotification(_ context.Context, _, _ int64, _ time.Time, code string) error {
	s.retried++
	s.retryError = code
	return nil
}
func (s *notificationStoreStub) ReleaseEnforcementNotification(context.Context, int64, int64, time.Time) error {
	s.released++
	return nil
}
func (s *notificationStoreStub) PruneEnforcementHistory(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

type notificationSenderStub struct {
	calls []service.NotificationEmailSendInput
	err   error
}

func (s *notificationSenderStub) Send(_ context.Context, in service.NotificationEmailSendInput) error {
	s.calls = append(s.calls, in)
	return s.err
}
func (s *notificationSenderStub) ResolveRecipientLocale(context.Context, int64, string) string {
	return "zh"
}

func TestPromptNotificationWorkerCommittedDelivery(t *testing.T) {
	store := &notificationStoreStub{n: &EnforcementNotification{ID: 5, ClaimVersion: 2, DeliveryVersion: 4, UserID: 7, Email: "test@example.com", HitCount: 3, ViolationCount: 9, WindowHours: 24, BanThreshold: 10, Categories: []string{"jailbreak", "combination"}, RuleGroupNames: []string{"example rule"}}, current: true}
	sender := &notificationSenderStub{}
	w := newPromptNotificationWorker(store, sender)
	require.True(t, w.deliverOne(context.Background()))
	require.Equal(t, 1, store.completed)
	require.Zero(t, store.retried)
	require.Len(t, sender.calls, 1)
	require.Equal(t, store.n.Email, store.checkedEmail, "validate the claimed recipient before delivery")
	in := sender.calls[0]
	require.Equal(t, service.NotificationEmailEventPromptAuditWarning, in.Event)
	require.Equal(t, "5:4", in.SourceID)
	require.Equal(t, "9", in.Variables["violation_count"])
	require.Equal(t, "3", in.Variables["new_hit_count"])
	require.Nil(t, in.RawHTMLVariables)
	require.Len(t, in.Variables, 7)
	store.n.AutoBanned = true
	require.True(t, w.deliverOne(context.Background()))
	require.Equal(t, service.NotificationEmailEventPromptAuditBanned, sender.calls[1].Event)
}

func TestPromptNotificationWorkerRejectsInvalidatedClaims(t *testing.T) {
	for _, checkErr := range []error{nil, errors.New("database down")} {
		store := &notificationStoreStub{n: &EnforcementNotification{ID: 5}, checkErr: checkErr}
		sender := &notificationSenderStub{}
		newPromptNotificationWorker(store, sender).deliverOne(context.Background())
		require.Empty(t, sender.calls)
		require.Zero(t, store.completed)
		require.Zero(t, store.retried)
		if checkErr == nil {
			require.Equal(t, 1, store.released)
		} else {
			require.Zero(t, store.released)
		}
	}
}

func TestPromptNotificationWorkerRetryDoesNotStoreSMTPError(t *testing.T) {
	store := &notificationStoreStub{n: &EnforcementNotification{ID: 5}, current: true}
	sender := &notificationSenderStub{err: errors.New("smtp password=secret recipient=private@example.com")}
	require.True(t, newPromptNotificationWorker(store, sender).deliverOne(context.Background()))
	require.Equal(t, 1, store.retried)
	require.Zero(t, store.completed)
	require.Equal(t, "notification_send_failed", store.retryError)
}

func TestPromptNotificationWorkerShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newPromptNotificationWorker(&notificationStoreStub{}, &notificationSenderStub{})
	w.Start(ctx)
	w.Start(ctx)
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	require.NoError(t, w.Shutdown(deadline))
	require.NoError(t, w.Shutdown(deadline))
}
