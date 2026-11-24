package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/topupproduct"
	"github.com/Wei-Shaw/sub2api/ent/topupsku"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

const TopupOrderType = "subscription_topup"

type SubscriptionTopupService struct {
	client        *dbent.Client
	encryptor     SecretEncryptor
	keyConfigured bool
}

func NewSubscriptionTopupService(client *dbent.Client, encryptor SecretEncryptor, cfg *config.Config) *SubscriptionTopupService {
	return &SubscriptionTopupService{client: client, encryptor: encryptor, keyConfigured: cfg != nil && cfg.Totp.EncryptionKeyConfigured}
}

type TopupProductInput struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Instructions *string `json:"instructions"`
	Enabled      *bool   `json:"enabled"`
	SortOrder    *int    `json:"sort_order"`
}
type TopupSKUInput struct {
	Name      *string  `json:"name"`
	Price     *float64 `json:"price"`
	Enabled   *bool    `json:"enabled"`
	SortOrder *int     `json:"sort_order"`
}
type TopupProductDTO struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Instructions string        `json:"instructions"`
	Enabled      bool          `json:"enabled"`
	SortOrder    int           `json:"sort_order"`
	SKUs         []TopupSKUDTO `json:"skus"`
}
type TopupSKUDTO struct {
	ID             int64   `json:"id"`
	ProductID      int64   `json:"product_id"`
	Name           string  `json:"name"`
	Price          float64 `json:"price"`
	Currency       string  `json:"currency"`
	Enabled        bool    `json:"enabled"`
	SortOrder      int     `json:"sort_order"`
	AvailableStock int     `json:"available_stock"`
}
type TopupSnapshot struct {
	ProductID    int64   `json:"product_id"`
	ProductName  string  `json:"product_name"`
	SKUID        int64   `json:"sku_id"`
	SKUName      string  `json:"sku_name"`
	UnitPrice    float64 `json:"unit_price"`
	Quantity     int     `json:"quantity"`
	Currency     string  `json:"currency"`
	Instructions string  `json:"instructions"`
	Locale       string  `json:"locale,omitempty"`
}
type TopupQuote struct {
	SKUID        int64
	Quantity     int
	UnitPrice    float64
	Amount       float64
	ProductName  string
	SKUName      string
	Instructions string
	Locale       string
}

func (s *SubscriptionTopupService) requireKey() error {
	if !s.keyConfigured {
		return infraerrors.BadRequest("TOPUP_ENCRYPTION_KEY_REQUIRED", "configure a persistent encryption key before importing or selling cards")
	}
	return nil
}

func validateTopupName(name *string, required bool) error {
	if name == nil && !required {
		return nil
	}
	if name == nil || strings.TrimSpace(*name) == "" || utf8.RuneCountInString(*name) > 100 {
		return infraerrors.BadRequest("INVALID_TOPUP_NAME", "name must contain 1 to 100 characters")
	}
	return nil
}

func validateTopupText(text *string) error {
	if text != nil && (!utf8.ValidString(*text) || len(*text) > 16384) {
		return infraerrors.BadRequest("INVALID_TOPUP_TEXT", "text must be valid UTF-8 and at most 16384 bytes")
	}
	return nil
}

func validateTopupPrice(price *float64, required bool) error {
	if price == nil && !required {
		return nil
	}
	if price == nil || math.IsNaN(*price) || math.IsInf(*price, 0) || *price <= 0 || *price > 1e12 {
		return infraerrors.BadRequest("INVALID_TOPUP_PRICE", "price must be positive and at most 1000000000000 CNY")
	}

	value := decimal.NewFromFloat(*price)
	if !value.Equal(value.Round(2)) {
		return infraerrors.BadRequest("INVALID_TOPUP_PRICE", "price supports at most two decimal places")
	}
	return nil
}

func (s *SubscriptionTopupService) validateProductInput(in TopupProductInput, create bool) error {
	if err := validateTopupName(in.Name, create); err != nil {
		return err
	}
	if err := validateTopupText(in.Description); err != nil {
		return err
	}
	if err := validateTopupText(in.Instructions); err != nil {
		return err
	}
	if in.Enabled != nil && *in.Enabled {
		return s.requireKey()
	}
	return nil
}

func (s *SubscriptionTopupService) validateSKUInput(in TopupSKUInput, create bool) error {
	if err := validateTopupName(in.Name, create); err != nil {
		return err
	}
	if err := validateTopupPrice(in.Price, create); err != nil {
		return err
	}
	if in.Enabled != nil && *in.Enabled {
		return s.requireKey()
	}
	return nil
}

func topupProductDTO(p *dbent.TopupProduct) *TopupProductDTO {
	return &TopupProductDTO{ID: p.ID, Name: p.Name, Description: p.Description, Instructions: p.Instructions, Enabled: p.Enabled, SortOrder: p.SortOrder, SKUs: []TopupSKUDTO{}}
}

func topupSKUDTO(sku *dbent.TopupSKU, stock int) *TopupSKUDTO {
	return &TopupSKUDTO{ID: sku.ID, ProductID: sku.ProductID, Name: sku.Name, Price: sku.Price, Currency: "CNY", Enabled: sku.Enabled, SortOrder: sku.SortOrder, AvailableStock: stock}
}

func (s *SubscriptionTopupService) ListProducts(ctx context.Context, admin bool) ([]TopupProductDTO, error) {
	q := s.client.TopupProduct.Query().Where(topupproduct.ArchivedAtIsNil()).Order(dbent.Asc(topupproduct.FieldSortOrder), dbent.Asc(topupproduct.FieldID))
	if !admin {
		q.Where(topupproduct.Enabled(true))
	}

	products, err := q.All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]TopupProductDTO, 0, len(products))
	if len(products) == 0 {
		return result, nil
	}

	ids := make([]int64, 0, len(products))
	indices := make(map[int64]int, len(products))

	for _, p := range products {
		indices[p.ID] = len(result)
		result = append(result, *topupProductDTO(p))
		ids = append(ids, p.ID)
	}

	sq := s.client.TopupSKU.Query().Where(topupsku.ProductIDIn(ids...), topupsku.ArchivedAtIsNil()).Order(dbent.Asc(topupsku.FieldSortOrder), dbent.Asc(topupsku.FieldID))
	if !admin {
		sq.Where(topupsku.Enabled(true))
	}

	skus, err := sq.All(ctx)
	if err != nil {
		return nil, err
	}

	stocks, err := s.stockCounts(ctx, ids)
	if err != nil {
		return nil, err
	}

	for _, sku := range skus {
		idx := indices[sku.ProductID]
		result[idx].SKUs = append(result[idx].SKUs, *topupSKUDTO(sku, stocks[sku.ID]))
	}
	return result, nil
}

func (s *SubscriptionTopupService) stockCounts(ctx context.Context, productIDs []int64) (map[int64]int, error) {
	// Query only IDs and counts; ciphertext never enters catalogue responses.

	rows, err := s.client.QueryContext(ctx, `SELECT s.id,GREATEST(0,
 (SELECT COUNT(*) FROM topup_cards c WHERE c.sku_id=s.id AND c.status='available')-
 COALESCE((SELECT SUM(o.topup_quantity) FROM payment_orders o WHERE o.topup_sku_id=s.id AND o.status='PAID' AND o.topup_delivery_status='waiting_stock'),0))
 FROM topup_skus s WHERE s.product_id = ANY($1::bigint[])`, pq.Array(productIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64]int)

	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		result[id] = n
	}
	return result, rows.Err()
}

func (s *SubscriptionTopupService) CreateProduct(ctx context.Context, in TopupProductInput) (*TopupProductDTO, error) {
	if err := s.validateProductInput(in, true); err != nil {
		return nil, err
	}

	b := s.client.TopupProduct.Create().SetName(strings.TrimSpace(*in.Name))
	if in.Description != nil {
		b.SetDescription(*in.Description)
	}
	if in.Instructions != nil {
		b.SetInstructions(*in.Instructions)
	}
	if in.Enabled != nil {
		b.SetEnabled(*in.Enabled)
	}
	if in.SortOrder != nil {
		b.SetSortOrder(*in.SortOrder)
	}

	p, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return topupProductDTO(p), nil
}

func (s *SubscriptionTopupService) UpdateProduct(ctx context.Context, id int64, in TopupProductInput) (*TopupProductDTO, error) {
	if err := s.validateProductInput(in, false); err != nil {
		return nil, err
	}

	p, err := s.client.TopupProduct.Query().Where(topupproduct.ID(id), topupproduct.ArchivedAtIsNil()).Only(ctx)
	if err != nil {
		return nil, topupNotFound(err)
	}

	b := p.Update()
	if in.Name != nil {
		b.SetName(strings.TrimSpace(*in.Name))
	}
	if in.Description != nil {
		b.SetDescription(*in.Description)
	}
	if in.Instructions != nil {
		b.SetInstructions(*in.Instructions)
	}
	if in.Enabled != nil {
		b.SetEnabled(*in.Enabled)
	}
	if in.SortOrder != nil {
		b.SetSortOrder(*in.SortOrder)
	}

	p, err = b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return topupProductDTO(p), nil
}

func (s *SubscriptionTopupService) ArchiveProduct(ctx context.Context, id int64) error {
	return s.client.TopupProduct.UpdateOneID(id).SetEnabled(false).SetArchivedAt(time.Now()).Exec(ctx)
}

func (s *SubscriptionTopupService) CreateSKU(ctx context.Context, productID int64, in TopupSKUInput) (*TopupSKUDTO, error) {
	if err := s.validateSKUInput(in, true); err != nil {
		return nil, err
	}
	if _, err := s.client.TopupProduct.Query().Where(topupproduct.ID(productID), topupproduct.ArchivedAtIsNil()).Only(ctx); err != nil {
		return nil, topupNotFound(err)
	}

	b := s.client.TopupSKU.Create().SetProductID(productID).SetName(strings.TrimSpace(*in.Name)).SetPrice(*in.Price)
	if in.Enabled != nil {
		b.SetEnabled(*in.Enabled)
	}
	if in.SortOrder != nil {
		b.SetSortOrder(*in.SortOrder)
	}

	sku, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return topupSKUDTO(sku, 0), nil
}

func (s *SubscriptionTopupService) UpdateSKU(ctx context.Context, id int64, in TopupSKUInput) (*TopupSKUDTO, error) {
	if err := s.validateSKUInput(in, false); err != nil {
		return nil, err
	}

	sku, err := s.client.TopupSKU.Query().Where(topupsku.ID(id), topupsku.ArchivedAtIsNil()).Only(ctx)
	if err != nil {
		return nil, topupNotFound(err)
	}

	b := sku.Update()
	if in.Name != nil {
		b.SetName(strings.TrimSpace(*in.Name))
	}
	if in.Price != nil {
		b.SetPrice(*in.Price)
	}
	if in.Enabled != nil {
		b.SetEnabled(*in.Enabled)
	}
	if in.SortOrder != nil {
		b.SetSortOrder(*in.SortOrder)
	}

	sku, err = b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return topupSKUDTO(sku, 0), nil
}

func (s *SubscriptionTopupService) ArchiveSKU(ctx context.Context, id int64) error {
	return s.client.TopupSKU.UpdateOneID(id).SetEnabled(false).SetArchivedAt(time.Now()).Exec(ctx)
}

func topupNotFound(err error) error {
	if dbent.IsNotFound(err) {
		return infraerrors.NotFound("TOPUP_NOT_FOUND", "product, specification or order not found")
	}
	return err
}

func topupCodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
