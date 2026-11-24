package service

import (
	"context"
	"html"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionTopupMailTemplateEscapesFullDelivery(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	info, _, err := svc.eventInfo(NotificationEmailEventSubscriptionTopupDelivery)
	require.NoError(t, err)
	require.False(t, info.Optional)
	for _, locale := range []string{"zh", "en"} {
		t.Run(locale, func(t *testing.T) {
			cards := strings.Repeat(strings.Repeat("a", 511)+"<\n", 99) + strings.Repeat("b", 511) + ">"
			instructions := "first line\n<script>alert('x')</script>"
			preview, err := svc.PreviewTemplate(context.Background(), NotificationEmailPreviewInput{
				Event: NotificationEmailEventSubscriptionTopupDelivery, Locale: locale,
				Variables: map[string]string{"cards": cards, "instructions": instructions, "product_name": "<b>product</b>", "quantity": "100"},
			})
			require.NoError(t, err)
			require.Contains(t, preview.HTML, html.EscapeString(cards))
			require.Contains(t, preview.HTML, html.EscapeString(instructions))
			require.Contains(t, preview.HTML, "white-space:pre-wrap")
			require.NotContains(t, preview.HTML, "<script>")
			require.NotContains(t, preview.HTML, "<b>product</b>")
			require.NotContains(t, preview.HTML, "unsubscribe")
		})
	}
	values := svc.runtimeVariables(context.Background(), NotificationEmailEventSubscriptionTopupDelivery, "en", NotificationEmailSendInput{})
	for _, key := range []string{"cards", "quantity", "product_name", "sku_name", "instructions", "order_id"} {
		require.Empty(t, values[key], "runtime must not send preview data: %s", key)
	}
}

func TestSubscriptionTopupMailSendStableIDAndResend(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtp := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtp.settings()))
	svc := NewNotificationEmailService(repo, NewEmailService(repo, nil))
	item := &topupMailItem{orderID: 123, recipient: "buyer@example.com", locale: "en", messageID: "<topup-123@sub2api.local>"}
	delivery := &TopupDeliveryDTO{ProductName: "Subscription", SKUName: "Month", Quantity: 2, Cards: []string{"CARD<ONE>", "CARD&TWO"}, Instructions: "Frozen instructions"}
	input := topupMailInput(item, delivery)
	require.Nil(t, input.RawHTMLVariables)
	require.NoError(t, svc.Send(ctx, input))
	require.NoError(t, svc.Send(ctx, input))
	require.Equal(t, int64(1), smtp.messageCount())
	message, err := mail.ReadMessage(strings.NewReader(smtp.lastMessage()))
	require.NoError(t, err)
	require.Equal(t, item.messageID, message.Header.Get("Message-Id"))
	body := smtp.lastMessageBody(t)
	require.Contains(t, body, "CARD&lt;ONE&gt;\r\nCARD&amp;TWO")
	require.Contains(t, body, delivery.Instructions)
	item.messageID = "<topup-123-resend@sub2api.local>"
	require.NoError(t, svc.Send(ctx, topupMailInput(item, delivery)))
	require.Equal(t, int64(2), smtp.messageCount())
	require.Contains(t, smtp.lastMessageBody(t), "CARD&lt;ONE&gt;\r\nCARD&amp;TWO")
}

func TestSubscriptionTopupMailRetryDelayIsBounded(t *testing.T) {
	for attempt, expected := range map[int]time.Duration{0: 2 * time.Minute, 1: 2 * time.Minute, 2: 4 * time.Minute, 4: 16 * time.Minute, 5: 32 * time.Minute, 99: 32 * time.Minute} {
		require.Equal(t, expected, topupMailRetryDelay(attempt))
	}
}

func TestSubscriptionTopupMailCannotStartAfterStop(t *testing.T) {
	svc := NewSubscriptionTopupMailService(nil, nil, nil)
	svc.Stop()
	svc.Start()
	svc.Stop()
	require.False(t, svc.started)
}

func TestSubscriptionTopupMailConcurrentLifecycle(t *testing.T) {
	svc := NewSubscriptionTopupMailService(nil, nil, nil)
	svc.cancel()
	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() {
			svc.Start()
			svc.Stop()
		})
	}
	callers.Wait()
	svc.Stop()
}
