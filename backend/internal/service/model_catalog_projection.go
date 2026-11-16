package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// CatalogAccountProjectionReader omits tokens, proxies and relation graphs.
// Consumers hydrate only an actual upstream catalog source before authorization.
type CatalogAccountProjectionReader interface {
	ListCatalogAccountProjections(context.Context, int64) ([]Account, error)
}
type catalogAccountProjection struct {
	accounts []Account
	version  string
}

func loadCatalogProjection(ctx context.Context, repo AccountRepository, store *ModelCatalogStore, groupID int64) (*catalogAccountProjection, error) {
	reader, ok := repo.(CatalogAccountProjectionReader)
	if !ok {
		return nil, nil
	}
	runtimeVersion := xai.RuntimeModelMappingVersion()
	value, err := store.Load(ctx, fmt.Sprintf("projection:%d:%d", groupID, runtimeVersion), func(ctx context.Context) (any, int64, error) {
		accounts, err := reader.ListCatalogAccountProjections(ctx, groupID)
		if err != nil {
			return nil, 0, err
		}
		for i := range accounts {
			accounts[i].PrepareSharedSnapshot()
		}
		raw, err := json.Marshal(accounts)
		if err != nil {
			return nil, 0, err
		}
		// A content version is computed only on refresh, never once per model.
		version := fmt.Sprintf("%s:%d", codexModelsManifestBodyETag(raw), runtimeVersion)
		for i := range accounts {
			accounts[i].catalogProjectionVersion = version
		}
		return &catalogAccountProjection{accounts: accounts, version: version}, catalogProjectionEstimatedBytes(accounts), nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*catalogAccountProjection), nil
}

// Count the backing slab and parsed maps on refresh. The JSON encoding is only
// temporary; its length is not a reliable estimate for maps with many short keys.
func catalogProjectionEstimatedBytes(accounts []Account) int64 {
	size := int64(cap(accounts)) * int64(unsafe.Sizeof(Account{}))
	for i := range accounts {
		a := &accounts[i]
		size += int64(len(a.Platform)+len(a.Type)+len(a.Status)+len(a.catalogProjectionVersion)) + 256
		size += catalogJSONEstimatedBytes(a.Credentials) + catalogJSONEstimatedBytes(a.Extra)
		for key, value := range a.GetModelMapping() {
			size += int64(64 + len(key) + len(value))
		}
		for key, value := range a.GetCompactModelMapping() {
			size += int64(64 + len(key) + len(value))
		}
	}
	return size
}

func catalogJSONEstimatedBytes(value any) int64 {
	switch v := value.(type) {
	case map[string]any:
		var size int64 = 48
		for key, item := range v {
			size += int64(64+len(key)) + catalogJSONEstimatedBytes(item)
		}
		return size
	case []any:
		size := int64(cap(v)) * 16
		for _, item := range v {
			size += catalogJSONEstimatedBytes(item)
		}
		return size
	case string:
		return int64(len(v))
	default:
		return 16
	}
}
func visibleCatalogAccounts(accounts []Account, now time.Time) []Account {
	var visible []Account
	for i := range accounts {
		a := &accounts[i]
		if !a.IsActive() || !a.Schedulable || (a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now)) ||
			(a.TempUnschedulableUntil != nil && a.TempUnschedulableUntil.After(now)) ||
			(a.RateLimitResetAt != nil && a.RateLimitResetAt.After(now)) || (a.OverloadUntil != nil && a.OverloadUntil.After(now)) {
			if visible == nil {
				visible = make([]Account, 0, len(accounts)-1)
				visible = append(visible, accounts[:i]...)
			}
			continue
		}
		if visible != nil {
			visible = append(visible, *a)
		}
	}
	if visible == nil {
		return accounts
	}
	return visible
}
func catalogAccounts(ctx context.Context, repo AccountRepository, store *ModelCatalogStore, groupID int64) ([]Account, []Account, error) {
	p, err := loadCatalogProjection(ctx, repo, store, groupID)
	if err != nil {
		return nil, nil, err
	}
	if p == nil {
		return loadCodexGroupCatalogAccounts(ctx, repo, groupID)
	}
	// Both slices are read-only. Prepared mappings make the catalog helpers safe
	// for shared reads; only synthesis's explicit mapping rewrite takes copies.
	return visibleCatalogAccounts(p.accounts, time.Now()), p.accounts, nil
}
func (s *OpenAIGatewayService) loadCatalogAccounts(ctx context.Context, groupID int64) ([]Account, []Account, error) {
	return catalogAccounts(ctx, s.accountRepo, s.catalogStore(), groupID)
}
func (s *GatewayService) loadCatalogAccounts(ctx context.Context, groupID int64) ([]Account, []Account, error) {
	return catalogAccounts(ctx, s.accountRepo, s.catalogStore(), groupID)
}
