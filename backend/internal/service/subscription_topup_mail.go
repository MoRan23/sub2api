package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/topupmail"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const topupMailMaxAttempts = 5

type SubscriptionTopupMailService struct {
	db            *sql.DB
	topups        *SubscriptionTopupService
	notifications *NotificationEmailService
	ctx           context.Context
	cancel        context.CancelFunc
	lifecycleMu   sync.Mutex
	started       bool
	stopped       bool
	wg            sync.WaitGroup
}

func NewSubscriptionTopupMailService(db *sql.DB, topups *SubscriptionTopupService, notifications *NotificationEmailService) *SubscriptionTopupMailService {
	ctx, cancel := context.WithCancel(context.Background())
	return &SubscriptionTopupMailService{db: db, topups: topups, notifications: notifications, ctx: ctx, cancel: cancel}
}

func (s *SubscriptionTopupMailService) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.wg.Add(1)
	go s.run()
}

func (s *SubscriptionTopupMailService) Stop() {
	s.lifecycleMu.Lock()
	s.stopped = true
	s.cancel()
	s.lifecycleMu.Unlock()
	s.wg.Wait()
}

func (s *SubscriptionTopupMailService) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastReconcile time.Time
	for {
		if s.ctx.Err() != nil {
			return
		}
		if time.Since(lastReconcile) >= 30*time.Second {
			ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
			err := s.topups.Reconcile(ctx, 20)
			cancel()
			lastReconcile = time.Now()
			if err != nil && s.ctx.Err() == nil {
				slog.Warn("subscription top-up reconciliation failed")
			}
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		item, err := s.claim(ctx)
		cancel()
		if err != nil && s.ctx.Err() == nil {
			slog.Warn("subscription top-up mail claim failed")
		}
		if item != nil {
			s.deliver(item)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type topupMailItem struct {
	id, orderID int64
	recipient   string
	locale      string
	messageID   string
	claimToken  string
	attempts    int
}

func (s *SubscriptionTopupMailService) claim(ctx context.Context) (*topupMailItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Admission is globally bounded; the transaction ends before decrypting or SMTP.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('subscription_topup_mail',0))`); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE topup_mails SET status='failed',claim_token='',lease_until=NULL,last_error='delivery_attempts_exhausted',updated_at=NOW()
WHERE status='sending' AND lease_until<=NOW() AND attempts >= $1`, topupMailMaxAttempts); err != nil {
		return nil, err
	}
	item := &topupMailItem{claimToken: uuid.NewString()}
	err = tx.QueryRowContext(ctx, `WITH candidate AS (
 SELECT id FROM topup_mails WHERE ((status='queued' AND available_at<=NOW()) OR (status='sending' AND lease_until<=NOW()))
 AND attempts<$2 AND (SELECT COUNT(*) FROM topup_mails WHERE status='sending' AND lease_until>NOW())<2
 ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED
) UPDATE topup_mails m SET status='sending',claim_token=$1,lease_until=NOW()+INTERVAL '2 minutes',attempts=attempts+1,updated_at=NOW()
FROM candidate c WHERE m.id=c.id RETURNING m.id,m.order_id,m.recipient,m.locale,m.message_id,m.attempts`, item.claimToken, topupMailMaxAttempts).
		Scan(&item.id, &item.orderID, &item.recipient, &item.locale, &item.messageID, &item.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

func (s *SubscriptionTopupMailService) deliver(item *topupMailItem) {
	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
	defer cancel()
	var ownedID int64
	err := s.db.QueryRowContext(ctx, `UPDATE topup_mails SET lease_until=NOW()+INTERVAL '2 minutes',updated_at=NOW()
WHERE id=$1 AND claim_token=$2 AND status='sending' AND lease_until>NOW() RETURNING id`, item.id, item.claimToken).Scan(&ownedID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	stage := "delivery_read_failed"
	if err == nil && strings.TrimSpace(item.recipient) == "" {
		err = errors.New("delivery recipient is empty")
	}
	if err == nil {
		var delivery *TopupDeliveryDTO
		delivery, err = s.topups.GetOrderCards(ctx, item.orderID, 0)
		if err == nil && (len(delivery.Cards) == 0 || len(delivery.Cards) != delivery.Quantity) {
			err = errors.New("order delivery is incomplete")
		}
		if err == nil {
			stage = "mail_send_failed"
			err = s.notifications.Send(ctx, topupMailInput(item, delivery))
		}
	}
	cancel()
	status := "sent"
	lastError := ""
	if err != nil {
		status = "queued"
		lastError = stage
		if item.attempts >= topupMailMaxAttempts {
			status = "failed"
		}
		// SMTP errors can contain remote text. Store only a fixed diagnostic code.
		slog.Warn("subscription top-up mail delivery failed", "order_id", item.orderID, "attempt", item.attempts, "stage", stage)
	}
	finish, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_, finishErr := s.db.ExecContext(finish, `UPDATE topup_mails SET status=$1,last_error=$2,claim_token='',lease_until=NULL,available_at=$3,
sent_at=CASE WHEN $1='sent' THEN NOW() ELSE sent_at END,updated_at=NOW()
WHERE id=$4 AND claim_token=$5 AND status='sending'`, status, lastError, time.Now().Add(topupMailRetryDelay(item.attempts)), item.id, item.claimToken)
	if finishErr != nil {
		slog.Warn("subscription top-up mail completion failed", "order_id", item.orderID)
	}
}

func topupMailRetryDelay(attempt int) time.Duration {
	return time.Duration(1<<min(max(attempt, 1), topupMailMaxAttempts)) * time.Minute
}

func topupMailInput(item *topupMailItem, delivery *TopupDeliveryDTO) NotificationEmailSendInput {
	return NotificationEmailSendInput{
		Event: NotificationEmailEventSubscriptionTopupDelivery, Locale: item.locale,
		RecipientEmail: item.recipient, RecipientName: item.recipient,
		SourceType: "subscription_topup", SourceID: strconv.FormatInt(item.orderID, 10), ReminderKey: item.messageID, MessageID: item.messageID,
		Variables: map[string]string{
			"product_name": delivery.ProductName, "sku_name": delivery.SKUName,
			"quantity": strconv.Itoa(delivery.Quantity), "cards": strings.Join(delivery.Cards, "\n"),
			"instructions": delivery.Instructions, "order_id": strconv.FormatInt(item.orderID, 10),
		},
	}
}

func (s *SubscriptionTopupService) QueueMailResend(ctx context.Context, orderID int64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(orderID)).ForUpdate().Only(ctx)
	if err != nil {
		return topupNotFound(err)
	}
	if order.OrderType != payment.OrderTypeSubscriptionTopup || order.Status != OrderStatusCompleted {
		return infraerrors.BadRequest("TOPUP_NOT_DELIVERED", "only delivered CDKs can be resent")
	}
	mail, err := tx.TopupMail.Query().Where(topupmail.OrderIDEQ(orderID)).ForUpdate().Only(ctx)
	if err != nil {
		return topupNotFound(err)
	}
	if mail.Status == "sending" && mail.LeaseUntil != nil && mail.LeaseUntil.After(time.Now()) {
		return infraerrors.Conflict("TOPUP_MAIL_SENDING", "card delivery email is already being sent")
	}
	if mail.Status == "queued" {
		return tx.Commit()
	}
	_, err = tx.TopupMail.UpdateOneID(mail.ID).SetStatus("queued").SetAttempts(0).
		SetAvailableAt(time.Now()).SetClaimToken("").ClearLeaseUntil().ClearSentAt().SetLastError("").
		SetMessageID(fmt.Sprintf("<topup-%s@sub2api.local>", uuid.NewString())).Save(ctx)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SubscriptionTopupService) MailStatus(ctx context.Context, orderID int64) (string, error) {
	mail, err := s.client.TopupMail.Query().Where(topupmail.OrderIDEQ(orderID)).Select(topupmail.FieldStatus).Only(ctx)
	if dbent.IsNotFound(err) {
		return "not_queued", nil
	}
	if err != nil {
		return "", err
	}
	return mail.Status, nil
}
