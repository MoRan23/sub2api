package securityaudit

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type enforcementNotificationStore interface {
	ClaimEnforcementNotification(context.Context, string, time.Time, time.Duration) (*EnforcementNotification, error)
	EnforcementNotificationCurrent(context.Context, int64, int64, string) (bool, error)
	CompleteEnforcementNotification(context.Context, int64, int64, time.Time) error
	RetryEnforcementNotification(context.Context, int64, int64, time.Time, string) error
	ReleaseEnforcementNotification(context.Context, int64, int64, time.Time) error
	PruneEnforcementHistory(context.Context, time.Time, int) (int64, error)
}

type promptNotificationSender interface {
	Send(context.Context, service.NotificationEmailSendInput) error
	ResolveRecipientLocale(context.Context, int64, string) string
}

// PromptNotificationWorker owns only small, committed outbox records. SMTP runs
// outside transactions; a two-minute lease outlives the bounded delivery call.
type PromptNotificationWorker struct {
	repo   enforcementNotificationStore
	sender promptNotificationSender
	id     string
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newPromptNotificationWorker(repo enforcementNotificationStore, sender promptNotificationSender) *PromptNotificationWorker {
	return &PromptNotificationWorker{repo: repo, sender: sender, id: uuid.NewString()}
}

func (w *PromptNotificationWorker) Start(parent context.Context) {
	if w == nil || w.repo == nil || w.sender == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel, w.done = cancel, make(chan struct{})
	go w.run(ctx, w.done)
}

func (w *PromptNotificationWorker) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		w.mu.Lock()
		w.cancel = nil
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *PromptNotificationWorker) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	maintenance := time.NewTicker(time.Hour)
	defer maintenance.Stop()
	for {
		for i := 0; i < 10 && ctx.Err() == nil; i++ {
			if !w.deliverOne(ctx) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-maintenance.C:
			cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = w.repo.PruneEnforcementHistory(cleanup, time.Now().UTC(), 1000)
			cancel()
		}
	}
}

func (w *PromptNotificationWorker) deliverOne(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	n, err := w.repo.ClaimEnforcementNotification(ctx, w.id, time.Now().UTC(), 2*time.Minute)
	if err != nil {
		if parent.Err() == nil {
			LogWarn("prompt_audit.notification_claim_failed", map[string]any{"error_code": "notification_claim_failed"})
		}
		return false
	}
	if n == nil {
		return false
	}
	locale := w.sender.ResolveRecipientLocale(ctx, n.UserID, n.Email)
	current, err := w.repo.EnforcementNotificationCurrent(ctx, n.ID, n.ClaimVersion, n.Email)
	if err != nil {
		return false
	} // Leave the lease for recovery; never send unverified state.
	if !current {
		_ = w.repo.ReleaseEnforcementNotification(ctx, n.ID, n.ClaimVersion, time.Now().UTC())
		return true
	}
	if err = w.sender.Send(ctx, promptNotificationInput(n, locale)); err != nil {
		if parent.Err() != nil {
			return false
		}
		ack, ackCancel := context.WithTimeout(parent, 5*time.Second)
		defer ackCancel()
		_ = w.repo.RetryEnforcementNotification(ack, n.ID, n.ClaimVersion, time.Now().UTC(), "notification_send_failed")
		LogWarn("prompt_audit.notification_send_failed", map[string]any{"notification_id": n.ID, "error_code": "notification_send_failed"})
		return true
	}
	ack, ackCancel := context.WithTimeout(parent, 5*time.Second)
	defer ackCancel()
	if err := w.repo.CompleteEnforcementNotification(ack, n.ID, n.ClaimVersion, time.Now().UTC()); err != nil && parent.Err() == nil {
		LogWarn("prompt_audit.notification_ack_failed", map[string]any{"notification_id": n.ID, "error_code": "notification_ack_failed"})
	}
	return true
}

func promptNotificationInput(n *EnforcementNotification, locale string) service.NotificationEmailSendInput {
	event := service.NotificationEmailEventPromptAuditWarning
	status := "Active"
	zh := strings.HasPrefix(strings.ToLower(locale), "zh")
	if zh {
		status = "启用"
	}
	if n.UserStatus != "" && n.UserStatus != "active" {
		status = "Disabled"
		if zh {
			status = "已停用"
		}
	}
	if n.AutoBanned {
		event = service.NotificationEmailEventPromptAuditBanned
		status = "Disabled by prompt auditing"
		if zh {
			status = "已被提示词审计封禁"
		}
	}
	categories := make([]string, 0, len(n.Categories))
	for _, category := range n.Categories {
		switch category {
		case "jailbreak":
			if zh {
				categories = append(categories, "破甲关键词")
			} else {
				categories = append(categories, "Jailbreak keywords")
			}
		case "combination":
			if zh {
				categories = append(categories, "主题与意图组合规则")
			} else {
				categories = append(categories, "Topic and intent rules")
			}
		}
	}
	groups := make([]string, 0, len(n.RuleGroupNames))
	for _, name := range n.RuleGroupNames {
		groups = append(groups, TrimRunes(strings.TrimSpace(name), 80))
	}
	return service.NotificationEmailSendInput{
		Event: event, Locale: locale, RecipientEmail: n.Email, RecipientName: n.Username, UserID: n.UserID,
		SourceType: "prompt_audit_notification", SourceID: strconv.FormatInt(n.ID, 10) + ":" + strconv.FormatInt(n.DeliveryVersion, 10),
		Variables: map[string]string{
			"categories": strings.Join(categories, ", "), "rule_group_names": strings.Join(groups, ", "),
			"new_hit_count": strconv.Itoa(n.HitCount), "violation_count": strconv.Itoa(n.ViolationCount),
			"window_hours": strconv.Itoa(n.WindowHours), "ban_threshold": strconv.Itoa(n.BanThreshold), "account_status": status,
		},
	}
}
