//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/topupcard"
	"github.com/Wei-Shaw/sub2api/ent/topupmail"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type topupIntegrationFixture struct {
	client    *dbent.Client
	service   *service.SubscriptionTopupService
	user      *dbent.User
	productID int64
}

func newTopupIntegrationFixture(t *testing.T) *topupIntegrationFixture {

	t.Helper()

	ctx := context.Background()

	client := testEntClient(t)

	svc := service.NewSubscriptionTopupService(client, nil, &config.Config{})

	user, err := client.User.Create().SetEmail(uuid.NewString() + "@example.test").SetPasswordHash("test").Save(ctx)

	require.NoError(t, err)

	name, instructions, enabled := "Subscription", "Use this code on the official site", true

	product, err := svc.CreateProduct(ctx, service.TopupProductInput{Name: &name, Instructions: &instructions, Enabled: &enabled})

	require.NoError(t, err)

	fixture := &topupIntegrationFixture{client: client, service: svc, user: user, productID: product.ID}

	t.Cleanup(func() {

		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM topup_mails WHERE order_id IN (SELECT id FROM payment_orders WHERE user_id=$1)`, user.ID)

		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM topup_cards WHERE sku_id IN (SELECT id FROM topup_skus WHERE product_id=$1)`, product.ID)

		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM payment_audit_logs WHERE order_id IN (SELECT id::text FROM payment_orders WHERE user_id=$1)`, user.ID)

		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM payment_orders WHERE user_id=$1`, user.ID)

		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM topup_skus WHERE product_id=$1`, product.ID)

		_ = client.TopupProduct.DeleteOneID(product.ID).Exec(ctx)

		_ = client.User.DeleteOneID(user.ID).Exec(ctx)

	})

	return fixture

}

func (f *topupIntegrationFixture) sku(t *testing.T, codes ...string) int64 {

	t.Helper()

	name, price, enabled := "Monthly", 12.34, true

	sku, err := f.service.CreateSKU(context.Background(), f.productID, service.TopupSKUInput{Name: &name, Price: &price, Enabled: &enabled})

	require.NoError(t, err)

	if len(codes) > 0 {

		_, err = f.service.ImportCards(context.Background(), sku.ID, codes)

		require.NoError(t, err)

	}

	return sku.ID

}

func (f *topupIntegrationFixture) reserve(ctx context.Context, skuID int64, quantity int) (*dbent.PaymentOrder, error) {

	quote, err := f.service.GetQuote(ctx, skuID, quantity)

	if err != nil {

		return nil, err
	}

	tx, err := f.client.Tx(ctx)

	if err != nil {

		return nil, err
	}

	defer tx.Rollback()

	order, err := tx.PaymentOrder.Create().SetUserID(f.user.ID).SetUserEmail(f.user.Email).SetUserName("test").SetAmount(quote.Amount).SetPayAmount(quote.Amount).SetRechargeCode(uuid.NewString()).SetPaymentType("alipay").SetPaymentTradeNo("").SetOrderType(service.TopupOrderType).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("localhost").Save(ctx)

	if err != nil {

		return nil, err
	}

	order, err = f.service.ReserveInTx(ctx, tx, order, quote)

	if err != nil {

		return nil, err
	}

	if err = tx.Commit(); err != nil {

		return nil, err
	}

	return order, nil

}

func TestSubscriptionTopupAtomicDeliveryAndDedup(t *testing.T) {

	ctx := context.Background()

	f := newTopupIntegrationFixture(t)

	code := "secret-" + uuid.NewString()

	sku := f.sku(t, code, "second-"+uuid.NewString())
	stored := f.client.TopupCard.Query().Where(topupcard.SkuID(sku)).Order(dbent.Asc(topupcard.FieldID)).FirstX(ctx)
	require.NotNil(t, stored.Code)
	require.Equal(t, code, *stored.Code)
	require.Empty(t, stored.Ciphertext)
	require.NotContains(t, stored.String(), code)
	require.NoError(t, f.service.SetCardEnabled(ctx, stored.ID, false))
	require.NoError(t, f.service.SetCardEnabled(ctx, stored.ID, true))
	revealed, err := f.service.RevealCard(ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, code, revealed)

	products, err := f.service.ListProducts(ctx, false)

	require.NoError(t, err)

	var stock int

	for _, product := range products {

		if product.ID == f.productID {

			stock = product.SKUs[0].AvailableStock
		}
	}

	require.Equal(t, 2, stock)

	order, err := f.reserve(ctx, sku, 1)

	require.NoError(t, err)

	page, err := f.service.ListCards(ctx, sku, "", 1, 20)

	require.NoError(t, err)

	require.Equal(t, 1, page.Counts["reserved"])

	require.NotContains(t, fmt.Sprint(page), code)

	require.NoError(t, f.service.ConfirmPayment(ctx, order.ID, "trade", 12.34))

	require.NoError(t, f.service.ConfirmPayment(ctx, order.ID, "trade", 12.34))

	delivery, err := f.service.GetOrderCards(ctx, order.ID, f.user.ID)

	require.NoError(t, err)

	require.Equal(t, []string{code}, delivery.Cards)
	mailDelivery, err := f.service.GetOrderCards(ctx, order.ID, 0)
	require.NoError(t, err)
	require.Equal(t, delivery.Cards, mailDelivery.Cards)

	_, err = f.service.GetOrderCards(ctx, order.ID, f.user.ID+100000)

	require.Error(t, err)

	require.Equal(t, 1, f.client.TopupMail.Query().Where(topupmail.OrderID(order.ID)).CountX(ctx))

	_, err = f.service.ImportCards(ctx, sku, []string{"new-" + uuid.NewString(), code})

	require.Error(t, err)

	require.Equal(t, 2, f.client.TopupCard.Query().Where(topupcard.SkuID(sku)).CountX(ctx))

	mail := f.client.TopupMail.Query().Where(topupmail.OrderID(order.ID)).OnlyX(ctx)

	require.NoError(t, f.service.QueueMailResend(ctx, order.ID))

	require.Equal(t, mail.MessageID, f.client.TopupMail.GetX(ctx, mail.ID).MessageID)

	require.NoError(t, f.client.TopupMail.UpdateOneID(mail.ID).SetStatus("sent").Exec(ctx))

	require.NoError(t, f.service.QueueMailResend(ctx, order.ID))

	require.NotEqual(t, mail.MessageID, f.client.TopupMail.GetX(ctx, mail.ID).MessageID)

	again, err := f.service.GetOrderCards(ctx, order.ID, f.user.ID)

	require.NoError(t, err)

	require.Equal(t, delivery.Cards, again.Cards)

}

func TestSubscriptionTopupConcurrentReservationAndLatePayment(t *testing.T) {

	ctx := context.Background()

	f := newTopupIntegrationFixture(t)

	sku := f.sku(t, "one-"+uuid.NewString())

	orders := make(chan *dbent.PaymentOrder, 2)

	errors := make(chan error, 2)

	var workers sync.WaitGroup

	for range 2 {

		workers.Add(1)
		go func() {

			defer workers.Done()
			order, err := f.reserve(ctx, sku, 1)
			if err != nil {

				errors <- err
				return
			}
			orders <- order
		}()
	}

	workers.Wait()
	close(orders)
	close(errors)

	require.Len(t, orders, 1)

	require.Len(t, errors, 1)

	first := <-orders

	require.NoError(t, f.service.ChangeUnpaidStatus(ctx, first.ID, service.OrderStatusExpired, "system", "expired"))

	second, err := f.reserve(ctx, sku, 1)

	require.NoError(t, err)

	require.NoError(t, f.service.ConfirmPayment(ctx, second.ID, "second", 12.34))

	require.NoError(t, f.service.ConfirmPayment(ctx, first.ID, "late", 12.34))

	require.Equal(t, "waiting_stock", f.client.PaymentOrder.GetX(ctx, first.ID).TopupDeliveryStatus)

	_, err = f.service.ImportCards(ctx, sku, []string{"late-stock-" + uuid.NewString()})

	require.NoError(t, err)

	_, err = f.reserve(ctx, sku, 1)

	require.Error(t, err)

	require.NoError(t, f.service.Reconcile(ctx, 20))

	require.Equal(t, service.OrderStatusCompleted, f.client.PaymentOrder.GetX(ctx, first.ID).Status)

}

func TestSubscriptionTopupPlainBase64CodeAndLegacyCiphertext(t *testing.T) {
	ctx := context.Background()
	f := newTopupIntegrationFixture(t)
	legacyCode := "legacy-" + uuid.NewString()
	plainCode := base64.StdEncoding.EncodeToString([]byte("plain-" + uuid.NewString() + strings.Repeat("X", 48)))
	skuID := f.sku(t, legacyCode, plainCode)
	cards := f.client.TopupCard.Query().Where(topupcard.SkuID(skuID)).Order(dbent.Asc(topupcard.FieldID)).AllX(ctx)
	require.Len(t, cards, 2)
	require.Equal(t, plainCode, *cards[1].Code)
	require.Empty(t, cards[1].Ciphertext)
	plain, err := f.service.RevealCard(ctx, cards[1].ID)
	require.NoError(t, err)
	require.Equal(t, plainCode, plain, "base64-shaped plaintext must not be treated as ciphertext")

	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = true
	cfg.Totp.EncryptionKey = strings.Repeat("ab", 32)
	encryptor, err := NewAESEncryptor(cfg)
	require.NoError(t, err)
	ciphertext, err := encryptor.Encrypt(legacyCode)
	require.NoError(t, err)
	legacy := f.client.TopupCard.UpdateOneID(cards[0].ID).ClearCode().SetCiphertext(ciphertext).SaveX(ctx)
	legacyService := service.NewSubscriptionTopupService(f.client, encryptor, cfg)
	revealed, err := legacyService.RevealCard(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, legacyCode, revealed)
	plain, err = legacyService.RevealCard(ctx, cards[1].ID)
	require.NoError(t, err)
	require.Equal(t, plainCode, plain)
	revealed, err = f.service.RevealCard(ctx, legacy.ID)
	require.Error(t, err)
	require.Equal(t, "TOPUP_LEGACY_ENCRYPTION_KEY_REQUIRED", infraerrors.Reason(err))
	require.Empty(t, revealed)
	require.NotContains(t, err.Error(), ciphertext)
	unchanged := f.client.TopupCard.GetX(ctx, legacy.ID)
	require.Nil(t, unchanged.Code)
	require.Equal(t, legacy.Ciphertext, unchanged.Ciphertext)
	require.Equal(t, legacy.CodeHash, unchanged.CodeHash)
	require.Equal(t, legacy.UpdatedAt, unchanged.UpdatedAt, "reading a legacy card must not rewrite it")

	order, err := f.reserve(ctx, skuID, 2)
	require.NoError(t, err)
	require.NoError(t, f.service.ConfirmPayment(ctx, order.ID, "mixed-format", 24.68))
	delivery, err := legacyService.GetOrderCards(ctx, order.ID, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, []string{legacyCode, plainCode}, delivery.Cards)
	mailDelivery, err := legacyService.GetOrderCards(ctx, order.ID, 0)
	require.NoError(t, err)
	require.Equal(t, delivery.Cards, mailDelivery.Cards)
	for _, userID := range []int64{f.user.ID, 0} {
		unreadable, err := f.service.GetOrderCards(ctx, order.ID, userID)
		require.Error(t, err)
		require.Equal(t, "TOPUP_LEGACY_ENCRYPTION_KEY_REQUIRED", infraerrors.Reason(err))
		require.Nil(t, unreadable)
		require.NotContains(t, err.Error(), ciphertext)
	}
	issued := f.client.TopupCard.GetX(ctx, legacy.ID)
	require.Nil(t, issued.Code)
	require.Equal(t, ciphertext, issued.Ciphertext)
	require.Equal(t, "issued", issued.Status)
	require.Equal(t, order.ID, *issued.OrderID)
	require.Equal(t, service.OrderStatusCompleted, f.client.PaymentOrder.GetX(ctx, order.ID).Status)
}

func TestSubscriptionTopupReconcileOtherSKUAndRollback(t *testing.T) {

	ctx := context.Background()

	f := newTopupIntegrationFixture(t)

	emptySKU := f.sku(t)

	stockedSKU := f.sku(t, "stock-"+uuid.NewString())

	for range 21 {

		f.client.PaymentOrder.Create().SetUserID(f.user.ID).SetUserEmail(f.user.Email).SetUserName("test").SetAmount(12.34).SetPayAmount(12.34).SetRechargeCode(uuid.NewString()).SetPaymentType("alipay").SetPaymentTradeNo("").SetOrderType(service.TopupOrderType).SetTopupSkuID(emptySKU).SetTopupQuantity(1).SetTopupDeliveryStatus("waiting_stock").SetStatus(service.OrderStatusPaid).SetPaidAt(time.Now().Add(-time.Hour)).SetExpiresAt(time.Now()).SetClientIP("127.0.0.1").SetSrcHost("localhost").SaveX(ctx)

	}

	order, err := f.reserve(ctx, stockedSKU, 1)

	require.NoError(t, err)

	require.NoError(t, f.service.ChangeUnpaidStatus(ctx, order.ID, service.OrderStatusExpired, "system", "expired"))

	require.NoError(t, f.client.PaymentOrder.UpdateOneID(order.ID).SetStatus(service.OrderStatusPaid).SetTopupDeliveryStatus("waiting_stock").SetPaidAt(time.Now()).Exec(ctx))

	require.NoError(t, f.service.Reconcile(ctx, 1))

	require.Equal(t, service.OrderStatusCompleted, f.client.PaymentOrder.GetX(ctx, order.ID).Status)

	sku := f.sku(t, "rollback-"+uuid.NewString())

	failed, err := f.reserve(ctx, sku, 1)

	require.NoError(t, err)

	f.client.TopupMail.Create().SetOrderID(failed.ID).SetRecipient(f.user.Email).SetMessageID("conflict").SaveX(ctx)

	require.Error(t, f.service.ConfirmPayment(ctx, failed.ID, "rollback", 12.34))

	require.Equal(t, service.OrderStatusPending, f.client.PaymentOrder.GetX(ctx, failed.ID).Status)

	require.Equal(t, 1, f.client.TopupCard.Query().Where(topupcard.OrderID(failed.ID), topupcard.Status("reserved")).CountX(ctx))

	require.Zero(t, f.client.TopupCard.Query().Where(topupcard.OrderID(failed.ID), topupcard.Status("issued")).CountX(ctx))

	_, err = f.client.PaymentOrder.Query().Where(paymentorder.ID(failed.ID)).Only(ctx)

	require.NoError(t, err)

}

func TestSubscriptionTopupMigrationRepeat(t *testing.T) {
	ctx := context.Background()
	initialSQL, err := os.ReadFile("../../migrations/307_subscription_topup.sql")
	require.NoError(t, err)
	plainSQL, err := os.ReadFile("../../migrations/308_subscription_topup_plain_codes.sql")
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	schema := "topup_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "SET LOCAL search_path TO "+schema)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `CREATE TABLE payment_orders(id BIGINT PRIMARY KEY,status TEXT,paid_at TIMESTAMPTZ)`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(initialSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO topup_products(id,name,enabled) VALUES(1,'Legacy product',true);
		INSERT INTO topup_skus(id,product_id,name,price,enabled) VALUES(1,1,'Monthly',12.34,true);
		INSERT INTO payment_orders(id,status,paid_at,topup_sku_id,topup_quantity,topup_delivery_status) VALUES(1,'COMPLETED','2026-11-01T00:00:00Z',1,1,'delivered');
		INSERT INTO topup_cards(id,sku_id,code_hash,ciphertext,masked_code,status,order_id,issued_at,created_at,updated_at)
		VALUES(1,1,'legacy-hash','legacy-encrypted-payload','le********ad','issued',1,'2026-11-01T00:00:00Z','2026-10-31T00:00:00Z','2026-11-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(plainSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO topup_cards(id,sku_id,code_hash,code,masked_code) VALUES(2,1,'plain-hash','ordinary-plaintext-card','or********rd')`)
	require.NoError(t, err, "new cards must not require a ciphertext value")
	for range 2 {
		_, err = tx.ExecContext(ctx, string(initialSQL))
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(plainSQL))
		require.NoError(t, err)
	}
	var oldCode sql.NullString
	var ciphertext, hash, status string
	var orderID int64
	var createdAt, updatedAt, issuedAt time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT code,ciphertext,code_hash,status,order_id,created_at,updated_at,issued_at FROM topup_cards WHERE id=1`).Scan(&oldCode, &ciphertext, &hash, &status, &orderID, &createdAt, &updatedAt, &issuedAt))
	require.False(t, oldCode.Valid)
	require.Equal(t, "legacy-encrypted-payload", ciphertext)
	require.Equal(t, "legacy-hash", hash)
	require.Equal(t, "issued", status)
	require.Equal(t, int64(1), orderID)
	require.Equal(t, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), createdAt.UTC())
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), updatedAt.UTC())
	require.Equal(t, updatedAt, issuedAt)
	var newCode string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT code,ciphertext FROM topup_cards WHERE id=2`).Scan(&newCode, &ciphertext))
	require.Equal(t, "ordinary-plaintext-card", newCode)
	require.Empty(t, ciphertext)
	var deliveryStatus string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT status,topup_delivery_status FROM payment_orders WHERE id=1`).Scan(&status, &deliveryStatus))
	require.Equal(t, "COMPLETED", status)
	require.Equal(t, "delivered", deliveryStatus)
}
