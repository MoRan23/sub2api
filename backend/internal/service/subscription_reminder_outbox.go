package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const subscriptionReminderBatchSize = 200

type subscriptionReminder struct {
	id, subscriptionID, userID int64
	email                      string
	days                       int
	expiresAt                  time.Time
	claim                      string
	attempts                   int
}

// Only finite, currently due subscriptions enter this projection. Keyset paging
// and a fixed observation time avoid COUNTs, full subscription graphs and pages
// shifting while expiry maintenance runs.
func (s *SubscriptionExpiryService) enqueueExpiryReminders(ctx context.Context, now time.Time) error {
	var cursorID int64
	cursorExpiry := now
	for batch := 0; batch < 20; batch++ {
		rows, err := s.db.QueryContext(ctx, `
SELECT s.id,s.user_id,u.email,s.expires_at,
 CASE WHEN s.expires_at <= $1::timestamptz + INTERVAL '1 day' THEN 1
      WHEN s.expires_at <= $1::timestamptz + INTERVAL '3 days' THEN 3 ELSE 7 END
FROM user_subscriptions s
JOIN users u ON u.id=s.user_id AND u.deleted_at IS NULL
JOIN groups g ON g.id=s.group_id AND g.deleted_at IS NULL
WHERE s.deleted_at IS NULL AND s.status='active' AND u.email<>''
 AND ((s.expires_at > $1 AND s.expires_at <= $1::timestamptz + INTERVAL '1 day')
   OR (s.expires_at > $1::timestamptz + INTERVAL '2 days' AND s.expires_at <= $1::timestamptz + INTERVAL '3 days')
   OR (s.expires_at > $1::timestamptz + INTERVAL '6 days' AND s.expires_at <= $1::timestamptz + INTERVAL '7 days'))
 AND (s.expires_at,s.id)>($2,$3)
 AND NOT EXISTS (SELECT 1 FROM subscription_reminder_outbox n WHERE n.subscription_id=s.id AND n.recipient_email=u.email
   AND n.reminder_days=CASE WHEN s.expires_at <= $1::timestamptz + INTERVAL '1 day' THEN 1 WHEN s.expires_at <= $1::timestamptz + INTERVAL '3 days' THEN 3 ELSE 7 END
   AND (n.status<>'skipped' OR n.expires_at=s.expires_at))
ORDER BY s.expires_at,s.id LIMIT $4`, now, cursorExpiry, cursorID, subscriptionReminderBatchSize)
		if err != nil {
			return err
		}
		items := make([]subscriptionReminder, 0, subscriptionReminderBatchSize)
		for rows.Next() {
			var item subscriptionReminder
			if err := rows.Scan(&item.subscriptionID, &item.userID, &item.email, &item.expiresAt, &item.days); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		// One transaction/statement per bounded page. Repeated scans are no-op
		// conflicts; a renewed subscription can replace an unsent stale reminder.
		args := make([]any, 0, len(items)*5)
		var query strings.Builder
		query.Grow(len(items)*36 + 512)
		query.WriteString(`INSERT INTO subscription_reminder_outbox(subscription_id,user_id,recipient_email,expires_at,reminder_days) VALUES `)
		for i, item := range items {
			if i > 0 {
				query.WriteByte(',')
			}
			n := len(args)
			fmt.Fprintf(&query, "($%d,$%d,$%d,$%d,$%d)", n+1, n+2, n+3, n+4, n+5)
			args = append(args, item.subscriptionID, item.userID, item.email, item.expiresAt, item.days)
		}
		query.WriteString(` ON CONFLICT(subscription_id,recipient_email,reminder_days) DO UPDATE SET
 expires_at=EXCLUDED.expires_at,status='queued',attempts=0,available_at=NOW(),updated_at=NOW()
 WHERE subscription_reminder_outbox.status='skipped' AND subscription_reminder_outbox.expires_at<>EXCLUDED.expires_at`)
		if _, err = s.db.ExecContext(ctx, query.String(), args...); err != nil {
			return err
		}
		last := items[len(items)-1]
		cursorID = last.subscriptionID
		cursorExpiry = last.expiresAt
		if len(items) < subscriptionReminderBatchSize {
			return nil
		}
	}
	return nil
}

func (s *SubscriptionExpiryService) claimReminder(ctx context.Context) (*subscriptionReminder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// A short transaction serializes only admission. SMTP never holds this lock.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(296,1)`); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE subscription_reminder_outbox SET status='failed',claim_id=NULL,lease_until=NULL,updated_at=NOW()
 WHERE status='running' AND lease_until<=NOW() AND attempts>=5`); err != nil {
		return nil, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_reminder_outbox WHERE status='running' AND lease_until>NOW()`).Scan(&active); err != nil {
		return nil, err
	}
	if active >= 2 {
		return nil, tx.Commit()
	}
	item := &subscriptionReminder{claim: uuid.NewString()}
	err = tx.QueryRowContext(ctx, `WITH candidate AS (
 SELECT id FROM subscription_reminder_outbox WHERE
 (status='queued' AND available_at<=NOW()) OR (status='running' AND lease_until<=NOW() AND attempts<5)
 ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED
) UPDATE subscription_reminder_outbox n SET status='running',claim_id=$1,lease_until=NOW()+INTERVAL '2 minutes',attempts=attempts+1,updated_at=NOW()
 FROM candidate c WHERE n.id=c.id
 RETURNING n.id,n.subscription_id,n.user_id,n.recipient_email,n.reminder_days,n.expires_at,n.attempts`, item.claim).
		Scan(&item.id, &item.subscriptionID, &item.userID, &item.email, &item.days, &item.expiresAt, &item.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

func (s *SubscriptionExpiryService) reminderWorker() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(s.runCtx, 5*time.Second)
		item, err := s.claimReminder(ctx)
		cancel()
		if err != nil {
			if s.runCtx.Err() == nil {
				log.Printf("[SubscriptionExpiry] Claim reminder failed: %v", err)
			}
			continue
		}
		if item == nil {
			continue
		}
		s.deliverReminder(item)
	}
}

func (s *SubscriptionExpiryService) deliverReminder(item *subscriptionReminder) {
	ctx, cancel := context.WithTimeout(s.runCtx, 45*time.Second)
	defer cancel()
	status := "sent"
	paused := false
	var name, groupName string
	var currentExpiry sql.NullTime
	// Reacquire ownership immediately before sending: a worker may have paused
	// after admission while another instance recovered its expired lease. Renew a
	// still-owned lease to cover the entire bounded SMTP operation.
	err := s.db.QueryRowContext(ctx, `WITH owned AS (
 UPDATE subscription_reminder_outbox SET lease_until=NOW()+INTERVAL '2 minutes',updated_at=NOW()
 WHERE id=$1 AND claim_id=$2 AND status='running' AND lease_until>NOW()
 RETURNING subscription_id
) SELECT COALESCE(checked.username,''),COALESCE(checked.name,''),checked.expires_at
FROM owned n LEFT JOIN LATERAL (
 SELECT u.username,g.name,s.expires_at
 FROM user_subscriptions s JOIN users u ON u.id=s.user_id AND u.deleted_at IS NULL
 JOIN groups g ON g.id=s.group_id AND g.deleted_at IS NULL
 WHERE s.id=n.subscription_id AND s.user_id=$3 AND u.email=$4 AND s.deleted_at IS NULL
  AND s.status='active' AND s.expires_at>NOW()
) checked ON TRUE`, item.id, item.claim, item.userID, item.email).
		Scan(&name, &groupName, &currentExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return // Lost ownership: do not send or change the new claimant's state.
	}
	if err == nil && (!currentExpiry.Valid || !currentExpiry.Time.Equal(item.expiresAt)) {
		status = "skipped"
		err = nil
	} else if err == nil && !s.expiryReminderEnabled(ctx) {
		// Retain the item for a later enabled cycle; disabling notifications is
		// not a successful delivery and must not consume the dedup key.
		status = "queued"
		paused = true
	} else if err == nil {
		err = s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
			Event: NotificationEmailEventSubscriptionExpiryReminder, RecipientEmail: item.email,
			RecipientName: firstNonEmpty(name, item.email), UserID: item.userID, SourceType: "user_subscription",
			SourceID: strconv.FormatInt(item.subscriptionID, 10), ReminderKey: fmt.Sprintf("%dd", item.days),
			Variables: map[string]string{"subscription_group": groupName, "expiry_time": item.expiresAt.Format("2006-01-02 15:04"), "days_remaining": strconv.Itoa(item.days)},
		})
	}
	cancel()
	if err != nil {
		status = "queued"
		if item.attempts >= 5 {
			status = "failed"
		}
		log.Printf("[SubscriptionExpiry] Reminder failed: subscription=%d attempt=%d err=%v", item.subscriptionID, item.attempts, err)
	}
	// Release only after all network work has returned, even on service shutdown.
	finish, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	delay := time.Duration(1<<min(item.attempts, 6)) * time.Minute
	if paused {
		delay = 5 * time.Minute
		item.attempts--
	}
	if _, finishErr := s.db.ExecContext(finish, `UPDATE subscription_reminder_outbox SET status=$1,claim_id=NULL,lease_until=NULL,available_at=$2,attempts=$5,updated_at=NOW() WHERE id=$3 AND claim_id=$4 AND status='running'`, status, time.Now().Add(delay), item.id, item.claim, item.attempts); finishErr != nil {
		log.Printf("[SubscriptionExpiry] Finish reminder failed: %v", finishErr)
	}
}
