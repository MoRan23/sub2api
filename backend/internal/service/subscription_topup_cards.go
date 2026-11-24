package service

import (
	"context"
	"fmt"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/topupcard"
	"github.com/Wei-Shaw/sub2api/ent/topupmail"
	"github.com/Wei-Shaw/sub2api/ent/topupsku"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"strings"
	"time"
	"unicode/utf8"
)

type TopupCardDTO struct {
	ID         int64      `json:"id"`
	SKUID      int64      `json:"sku_id"`
	MaskedCode string     `json:"masked_code"`
	Status     string     `json:"status"`
	OrderID    *int64     `json:"order_id"`
	CreatedAt  time.Time  `json:"created_at"`
	IssuedAt   *time.Time `json:"issued_at"`
}
type TopupCardPage struct {
	Items    []TopupCardDTO `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Counts   map[string]int `json:"counts"`
}
type TopupDeliveryDTO struct {
	OrderID        int64    `json:"order_id"`
	ProductName    string   `json:"product_name"`
	SKUName        string   `json:"sku_name"`
	Quantity       int      `json:"quantity"`
	Instructions   string   `json:"instructions"`
	Cards          []string `json:"cards"`
	DeliveryStatus string   `json:"delivery_status"`
	EmailStatus    string   `json:"email_status"`
}
type topupImportCard struct {
	code, hash, ciphertext, masked string
	line                           int
}

func prepareTopupCodes(codes []string) ([]topupImportCard, error) {
	if len(codes) > 1000 {
		return nil, infraerrors.BadRequest("TOPUP_IMPORT_TOO_LARGE", "at most 1000 lines per import")
	}

	result := make([]topupImportCard, 0, len(codes))
	seen := make(map[string]int, len(codes))

	for index, raw := range codes {
		code := strings.TrimSpace(raw)
		if code == "" {
			continue
		}
		if len(code) > 512 || !utf8.ValidString(code) || strings.ContainsAny(code, "\r\n\x00") {
			return nil, infraerrors.BadRequest("INVALID_TOPUP_CODE", fmt.Sprintf("invalid CDK on line %d", index+1))
		}

		hash := topupCodeHash(code)
		if prior, ok := seen[hash]; ok {
			return nil, infraerrors.BadRequest("DUPLICATE_TOPUP_CODE", fmt.Sprintf("duplicate CDK on lines %d and %d", prior, index+1))
		}

		seen[hash] = index + 1

		result = append(result, topupImportCard{code: code, hash: hash, masked: maskTopupCode(code), line: index + 1})
	}
	if len(result) == 0 {
		return nil, infraerrors.BadRequest("EMPTY_TOPUP_IMPORT", "no CDKs supplied")
	}
	return result, nil
}

func maskTopupCode(code string) string {
	runes := []rune(code)
	if len(runes) <= 8 {
		return "********"
	}
	return string(runes[:2]) + "********" + string(runes[len(runes)-2:])
}

func (s *SubscriptionTopupService) ImportCards(ctx context.Context, skuID int64, codes []string) (int, error) {
	if err := s.requireKey(); err != nil {
		return 0, err
	}

	prepared, err := prepareTopupCodes(codes)
	if err != nil {
		return 0, err
	}
	// Encrypt before entering the inventory transaction.

	for i := range prepared {
		prepared[i].ciphertext, err = s.encryptor.Encrypt(prepared[i].code)
		prepared[i].code = ""
		if err != nil {
			return 0, fmt.Errorf("encrypt CDK: %w", err)
		}
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.TopupSKU.Query().Where(topupsku.ID(skuID), topupsku.ArchivedAtIsNil()).ForUpdate().Only(ctx); err != nil {
		return 0, topupNotFound(err)
	}

	hashes := make([]string, len(prepared))
	for i, c := range prepared {
		hashes[i] = c.hash
	}

	existing, err := tx.TopupCard.Query().Where(topupcard.CodeHashIn(hashes...)).Select(topupcard.FieldCodeHash).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		duplicate := make(map[string]struct{}, len(existing))
		for _, c := range existing {
			duplicate[c.CodeHash] = struct{}{}
		}

		for _, c := range prepared {
			if _, ok := duplicate[c.hash]; ok {
				return 0, infraerrors.BadRequest("DUPLICATE_TOPUP_CODE", fmt.Sprintf("CDK on line %d already exists", c.line))
			}
		}
	}

	builders := make([]*dbent.TopupCardCreate, len(prepared))

	for i, c := range prepared {
		builders[i] = tx.TopupCard.Create().SetSkuID(skuID).SetCodeHash(c.hash).SetCiphertext(c.ciphertext).SetMaskedCode(c.masked)
	}
	if _, err = tx.TopupCard.CreateBulk(builders...).Save(ctx); err != nil {
		if dbent.IsConstraintError(err) {
			return 0, infraerrors.BadRequest("DUPLICATE_TOPUP_CODE", "a CDK was concurrently imported; no cards from this batch were saved")
		}
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(prepared), nil
}

func (s *SubscriptionTopupService) ListCards(ctx context.Context, skuID int64, status string, page, size int) (*TopupCardPage, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}

	switch status {
	case "", "available", "reserved", "issued", "disabled":
	default:
		return nil, infraerrors.BadRequest("INVALID_TOPUP_STATUS", "invalid card status")
	}

	q := s.client.TopupCard.Query().Where(topupcard.SkuID(skuID))
	if status != "" {
		q.Where(topupcard.Status(status))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}

	cards, err := q.Select(topupcard.FieldID, topupcard.FieldSkuID, topupcard.FieldMaskedCode, topupcard.FieldStatus, topupcard.FieldOrderID, topupcard.FieldCreatedAt, topupcard.FieldIssuedAt).Order(dbent.Desc(topupcard.FieldID)).Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, err
	}

	result := &TopupCardPage{Items: make([]TopupCardDTO, 0, len(cards)), Total: total, Page: page, PageSize: size, Counts: map[string]int{"available": 0, "reserved": 0, "issued": 0, "disabled": 0}}

	for _, c := range cards {
		result.Items = append(result.Items, TopupCardDTO{ID: c.ID, SKUID: c.SkuID, MaskedCode: c.MaskedCode, Status: c.Status, OrderID: c.OrderID, CreatedAt: c.CreatedAt, IssuedAt: c.IssuedAt})
	}

	rows, err := s.client.QueryContext(ctx, "SELECT status,COUNT(*) FROM topup_cards WHERE sku_id=$1 GROUP BY status", skuID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result.Counts[status] = count
	}
	return result, rows.Err()
}

func (s *SubscriptionTopupService) SetCardEnabled(ctx context.Context, id int64, enabled bool) error {
	if enabled {
		if err := s.requireKey(); err != nil {
			return err
		}
	}

	status := "disabled"
	if enabled {
		status = "available"
	}

	changed, err := s.client.TopupCard.Update().Where(topupcard.ID(id), topupcard.StatusIn("available", "disabled")).SetStatus(status).Save(ctx)
	if err != nil {
		return err
	}
	if changed == 0 {
		return infraerrors.Conflict("TOPUP_CARD_UNAVAILABLE", "reserved or issued cards cannot be changed")
	}
	return nil
}

func (s *SubscriptionTopupService) RevealCard(ctx context.Context, id int64) (string, error) {
	if err := s.requireKey(); err != nil {
		return "", err
	}

	c, err := s.client.TopupCard.Query().Where(topupcard.ID(id)).Select(topupcard.FieldCiphertext).Only(ctx)
	if err != nil {
		return "", topupNotFound(err)
	}

	code, err := s.encryptor.Decrypt(c.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt CDK: %w", err)
	}
	return code, nil
}

func (s *SubscriptionTopupService) GetOrderCards(ctx context.Context, orderID, userID int64) (*TopupDeliveryDTO, error) {
	order, err := s.client.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, topupNotFound(err)
	}
	if userID != 0 && order.UserID != userID {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission for this order")
	}
	if order.OrderType != TopupOrderType {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "not a subscription topup order")
	}

	snapshot := TopupOrderSnapshot(order)

	result := &TopupDeliveryDTO{OrderID: order.ID, Quantity: order.TopupQuantity, DeliveryStatus: order.TopupDeliveryStatus, Cards: []string{}, EmailStatus: "not_queued"}
	if snapshot != nil {
		result.ProductName = snapshot.ProductName
		result.SKUName = snapshot.SKUName
		result.Instructions = snapshot.Instructions
	}

	mail, err := s.client.TopupMail.Query().Where(topupmail.OrderID(order.ID)).Select(topupmail.FieldStatus).Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	if mail != nil {
		result.EmailStatus = mail.Status
	}
	if order.Status != OrderStatusCompleted {
		return result, nil
	}
	if err := s.requireKey(); err != nil {
		return nil, err
	}

	cards, err := s.client.TopupCard.Query().Where(topupcard.OrderID(order.ID), topupcard.Status("issued")).Select(topupcard.FieldCiphertext).Order(dbent.Asc(topupcard.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(cards) != order.TopupQuantity {
		return nil, infraerrors.InternalServer("TOPUP_DELIVERY_INCOMPLETE", "order delivery is incomplete")
	}

	for _, card := range cards {
		code, err := s.encryptor.Decrypt(card.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt delivered CDK: %w", err)
		}
		result.Cards = append(result.Cards, code)
	}
	return result, nil
}
