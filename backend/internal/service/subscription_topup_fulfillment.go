package service

import (
	"context"
	"encoding/json"
	"fmt"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/topupcard"
	"github.com/Wei-Shaw/sub2api/ent/topupsku"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
	"log/slog"
	"strconv"
	"time"
)

func TopupOrderSnapshot(order *dbent.PaymentOrder) *TopupSnapshot {
	if order == nil || len(order.TopupSnapshot) == 0 {
		return nil
	}

	raw, err := json.Marshal(order.TopupSnapshot)
	if err != nil {
		return nil
	}

	var snapshot TopupSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil
	}
	return &snapshot
}

func (s *SubscriptionTopupService) GetQuote(ctx context.Context, skuID int64, quantity int) (*TopupQuote, error) {
	if err := s.requireKey(); err != nil {
		return nil, err
	}
	if quantity < 1 || quantity > 100 {
		return nil, infraerrors.BadRequest("INVALID_TOPUP_QUANTITY", "quantity must be between 1 and 100")
	}

	sku, err := s.client.TopupSKU.Get(ctx, skuID)
	if err != nil {
		return nil, topupNotFound(err)
	}

	product, err := s.client.TopupProduct.Get(ctx, sku.ProductID)
	if err != nil {
		return nil, topupNotFound(err)
	}
	if !sku.Enabled || sku.ArchivedAt != nil || !product.Enabled || product.ArchivedAt != nil {
		return nil, infraerrors.BadRequest("TOPUP_NOT_FOR_SALE", "product or specification is not for sale")
	}

	amount, _ := decimal.NewFromFloat(sku.Price).Mul(decimal.NewFromInt(int64(quantity))).Float64()
	return &TopupQuote{SKUID: sku.ID, Quantity: quantity, UnitPrice: sku.Price, Amount: amount, ProductName: product.Name, SKUName: sku.Name, Instructions: product.Instructions}, nil
}

// Inventory writers serialize per SKU, then lock existing order rows, then cards.
// New order rows created by the caller are private until its transaction commits.
func (s *SubscriptionTopupService) ReserveInTx(ctx context.Context, tx *dbent.Tx, order *dbent.PaymentOrder, quote *TopupQuote) (*dbent.PaymentOrder, error) {
	if err := s.requireKey(); err != nil {
		return nil, err
	}

	sku, err := tx.TopupSKU.Query().Where(topupsku.ID(quote.SKUID)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, topupNotFound(err)
	}

	product, err := tx.TopupProduct.Get(ctx, sku.ProductID)
	if err != nil {
		return nil, err
	}
	if !sku.Enabled || sku.ArchivedAt != nil || !product.Enabled || product.ArchivedAt != nil {
		return nil, infraerrors.BadRequest("TOPUP_NOT_FOR_SALE", "product or specification is not for sale")
	}
	if sku.Price != quote.UnitPrice {
		return nil, infraerrors.Conflict("TOPUP_PRICE_CHANGED", "price changed; please refresh before placing the order")
	}

	rows, err := tx.QueryContext(ctx, `SELECT
 (SELECT COUNT(*) FROM topup_cards WHERE sku_id=$1 AND status='available') -
 COALESCE((SELECT SUM(topup_quantity) FROM payment_orders WHERE topup_sku_id=$1 AND status='PAID' AND topup_delivery_status='waiting_stock'),0)`, sku.ID)
	if err != nil {
		return nil, err
	}

	var available int
	if rows.Next() {
		err = rows.Scan(&available)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if available < quote.Quantity {
		return nil, infraerrors.Conflict("TOPUP_OUT_OF_STOCK", "not enough cards available")
	}

	ids, err := tx.TopupCard.Query().Where(topupcard.SkuID(sku.ID), topupcard.Status("available")).Order(dbent.Asc(topupcard.FieldID)).Limit(quote.Quantity).ForUpdate().IDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) != quote.Quantity {
		return nil, infraerrors.Conflict("TOPUP_OUT_OF_STOCK", "not enough cards available")
	}
	if _, err = tx.TopupCard.Update().Where(topupcard.IDIn(ids...)).SetStatus("reserved").SetOrderID(order.ID).Save(ctx); err != nil {
		return nil, err
	}

	locale := normalizeNotificationLocale(quote.Locale)

	snapshot := TopupSnapshot{ProductID: product.ID, ProductName: product.Name, SKUID: sku.ID, SKUName: sku.Name, UnitPrice: sku.Price, Quantity: quote.Quantity, Currency: "CNY", Instructions: product.Instructions, Locale: locale}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}

	fields := order.TopupSnapshot
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return tx.PaymentOrder.UpdateOneID(order.ID).SetTopupSkuID(sku.ID).SetTopupQuantity(quote.Quantity).SetTopupSnapshot(fields).SetTopupDeliveryStatus("reserved").Save(ctx)
}

func (s *SubscriptionTopupService) lockedOrder(ctx context.Context, tx *dbent.Tx, id int64) (*dbent.PaymentOrder, error) {
	projection, err := tx.PaymentOrder.Query().Where(paymentorder.ID(id)).Select(paymentorder.FieldTopupSkuID, paymentorder.FieldOrderType).Only(ctx)
	if err != nil {
		return nil, topupNotFound(err)
	}
	if projection.OrderType != TopupOrderType || projection.TopupSkuID == nil {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "not a subscription topup order")
	}
	if _, err = tx.TopupSKU.Query().Where(topupsku.ID(*projection.TopupSkuID)).Select(topupsku.FieldID).ForUpdate().Only(ctx); err != nil {
		return nil, err
	}
	return tx.PaymentOrder.Query().Where(paymentorder.ID(id)).ForUpdate().Only(ctx)
}

func (s *SubscriptionTopupService) ChangeUnpaidStatus(ctx context.Context, id int64, status, operator, detail string) error {
	switch status {
	case OrderStatusCancelled, OrderStatusExpired, OrderStatusFailed:
	default:
		return infraerrors.BadRequest("INVALID_STATUS", "invalid unpaid order transition")
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	order, err := s.lockedOrder(ctx, tx, id)
	if err != nil {
		return err
	}
	if order.Status != OrderStatusPending {
		return nil
	}
	if _, err = tx.TopupCard.Update().Where(topupcard.OrderID(id), topupcard.Status("reserved")).SetStatus("available").ClearOrderID().Save(ctx); err != nil {
		return err
	}
	update := tx.PaymentOrder.UpdateOneID(id).SetStatus(status).SetTopupDeliveryStatus("released")
	if status == OrderStatusFailed {
		update.SetFailedAt(time.Now()).SetFailedReason(detail)
	}
	if err = update.Exec(ctx); err != nil {
		return err
	}

	action := "ORDER_CANCELLED"
	if status == OrderStatusExpired {
		action = "ORDER_EXPIRED"
	}
	if status == OrderStatusFailed {
		action = "FULFILLMENT_FAILED"
	}
	if err = tx.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(id, 10)).SetAction(action).SetOperator(operator).SetDetail(detail).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SubscriptionTopupService) ConfirmPayment(ctx context.Context, id int64, tradeNo string, paid float64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	order, err := s.lockedOrder(ctx, tx, id)
	if err != nil {
		return err
	}
	if order.Status == OrderStatusCompleted {
		return nil
	}

	switch order.Status {
	case OrderStatusPending, OrderStatusCancelled, OrderStatusExpired, OrderStatusFailed:

		previousStatus := order.Status

		order, err = tx.PaymentOrder.UpdateOneID(id).SetStatus(OrderStatusPaid).SetPaymentTradeNo(tradeNo).SetPaidAt(time.Now()).SetPayAmount(paid).ClearFailedAt().ClearFailedReason().Save(ctx)
		if err != nil {
			return err
		}
		if err = tx.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(id, 10)).SetAction("ORDER_PAID").SetOperator("system").SetDetail("Topup payment confirmed").Exec(ctx); err != nil {
			return err
		}
		if previousStatus != OrderStatusPending {
			if err = tx.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(id, 10)).SetAction("ORDER_RECOVERED").SetOperator("system").SetDetail(previousStatus).Exec(ctx); err != nil {
				return err
			}
		}

	case OrderStatusPaid, OrderStatusRecharging:

	default:
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot be fulfilled in current status")
	}
	if err = s.fulfillLocked(ctx, tx, order); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SubscriptionTopupService) Fulfill(ctx context.Context, id int64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	order, err := s.lockedOrder(ctx, tx, id)
	if err != nil {
		return err
	}
	if order.Status == OrderStatusCompleted {
		return nil
	}
	if order.Status != OrderStatusPaid && order.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order is not paid")
	}
	if err = s.fulfillLocked(ctx, tx, order); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SubscriptionTopupService) fulfillLocked(ctx context.Context, tx *dbent.Tx, order *dbent.PaymentOrder) error {
	ids, err := tx.TopupCard.Query().Where(topupcard.OrderID(order.ID), topupcard.Status("reserved")).Order(dbent.Asc(topupcard.FieldID)).ForUpdate().IDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) != order.TopupQuantity {
		if len(ids) != 0 {
			return fmt.Errorf("topup reservation count mismatch for order %d", order.ID)
		}
		if err = tx.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusPaid).SetTopupDeliveryStatus("waiting_stock").Exec(ctx); err != nil {
			return err
		}

		first, err := tx.PaymentOrder.Query().Where(paymentorder.TopupSkuID(*order.TopupSkuID), paymentorder.Status(OrderStatusPaid), paymentorder.TopupDeliveryStatus("waiting_stock")).Order(dbent.Asc(paymentorder.FieldPaidAt), dbent.Asc(paymentorder.FieldID)).Select(paymentorder.FieldID).First(ctx)
		if err != nil {
			return err
		}
		if first.ID != order.ID {
			return nil
		}

		ids, err = tx.TopupCard.Query().Where(topupcard.SkuID(*order.TopupSkuID), topupcard.Status("available")).Order(dbent.Asc(topupcard.FieldID)).Limit(order.TopupQuantity).ForUpdate().IDs(ctx)
		if err != nil {
			return err
		}
		if len(ids) != order.TopupQuantity {
			return nil
		}
	}

	now := time.Now()
	if _, err = tx.TopupCard.Update().Where(topupcard.IDIn(ids...)).SetStatus("issued").SetOrderID(order.ID).SetIssuedAt(now).Save(ctx); err != nil {
		return err
	}
	if err = tx.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).SetTopupDeliveryStatus("delivered").SetCompletedAt(now).Exec(ctx); err != nil {
		return err
	}

	locale := "zh"
	if snapshot := TopupOrderSnapshot(order); snapshot != nil && snapshot.Locale != "" {
		locale = snapshot.Locale
	}
	if err = tx.TopupMail.Create().SetOrderID(order.ID).SetRecipient(order.UserEmail).SetLocale(locale).SetMessageID(fmt.Sprintf("<topup-order-%d@sub2api.local>", order.ID)).Exec(ctx); err != nil {
		return err
	}
	return tx.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(order.ID, 10)).SetAction("TOPUP_DELIVERED").SetOperator("system").SetDetail(fmt.Sprintf("Delivered %d cards", order.TopupQuantity)).Exec(ctx)
}

func (s *SubscriptionTopupService) Reconcile(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 50
	}

	rows, err := s.client.QueryContext(ctx, `SELECT o.id FROM (
 SELECT DISTINCT ON (topup_sku_id) id,topup_sku_id,topup_quantity,paid_at FROM payment_orders
 WHERE status='PAID' AND topup_delivery_status='waiting_stock'
 ORDER BY topup_sku_id,paid_at,id
 ) o WHERE (SELECT COUNT(*) FROM topup_cards c WHERE c.sku_id=o.topup_sku_id AND c.status='available')>=o.topup_quantity
 ORDER BY o.paid_at,o.id LIMIT $1`, limit)
	if err != nil {
		return err
	}

	ids := make([]int64, 0, limit)

	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}

	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Fulfill(ctx, id); err != nil {
			slog.Error("topup delivery retry failed", "order_id", id, "error", err)
		}
	}
	return nil
}
