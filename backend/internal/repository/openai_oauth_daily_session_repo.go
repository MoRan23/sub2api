package repository

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/openaioauthdailysessionpool"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type openAIOAuthDailySessionRepository struct{ client *ent.Client }

var _ service.OAuthDailySessionOSRepository = (*openAIOAuthDailySessionRepository)(nil)

func NewOpenAIOAuthDailySessionRepository(client *ent.Client) service.OAuthDailySessionRepository {
	return &openAIOAuthDailySessionRepository{client: client}
}

func (r *openAIOAuthDailySessionRepository) ownerAccountID(ctx context.Context, id int64) (int64, error) {
	if id <= 0 {
		return 0, fmt.Errorf("invalid OAuth account id %d", id)
	}
	a, err := r.client.Account.Query().Where(account.IDEQ(id)).Only(ctx)
	if err != nil {
		return 0, fmt.Errorf("resolve OAuth account %d: %w", id, err)
	}
	if a.ParentAccountID != nil && *a.ParentAccountID > 0 {
		return *a.ParentAccountID, nil
	}
	return a.ID, nil
}

func newUUIDv7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (r *openAIOAuthDailySessionRepository) GetOrCreateOAuthDailySessionPool(ctx context.Context, accountID int64, now time.Time) (service.OAuthDailySessionPool, error) {
	if r == nil || r.client == nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("nil OpenAI OAuth daily-session repository")
	}
	owner, err := r.ownerAccountID(ctx, accountID)
	if err != nil {
		return service.OAuthDailySessionPool{}, err
	}
	date := service.OAuthDailyBusinessDate(now)
	generation, err := newUUIDv7()
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("generate OAuth generation: %w", err)
	}
	roots := [4]string{}
	for i := range roots {
		roots[i], err = newUUIDv7()
		if err != nil {
			return service.OAuthDailySessionPool{}, fmt.Errorf("generate OAuth daily root: %w", err)
		}
	}
	rows, err := r.client.QueryContext(ctx, `
			INSERT INTO openai_oauth_daily_session_pools
			(account_id,business_date,generation,stream_session_0,stream_session_1,stream_session_2,sync_session,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NOW(),NOW())
			ON CONFLICT (account_id,business_date) DO UPDATE SET updated_at=NOW()
			RETURNING business_date,generation,stream_session_0,stream_session_1,stream_session_2,sync_session`, owner, date, generation, roots[0], roots[1], roots[2], roots[3])
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("create OAuth daily session pool for account %d date %s: %w", owner, date, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return service.OAuthDailySessionPool{}, fmt.Errorf("read OAuth daily session pool for account %d date %s: %w", owner, date, err)
		}
		return service.OAuthDailySessionPool{}, fmt.Errorf("read OAuth daily session pool for account %d date %s: query returned no rows", owner, date)
	}
	var result service.OAuthDailySessionPool
	result.AccountID = owner
	if err := rows.Scan(&result.BusinessDate, &result.Generation, &result.StreamSessionIDs[0], &result.StreamSessionIDs[1], &result.StreamSessionIDs[2], &result.SyncSessionID); err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("scan OAuth daily session pool for account %d date %s: %w", owner, date, err)
	}
	return result, nil
}

// GetOrCreateOAuthDailySessionPoolForOS upgrades the legacy four-root pool in
// place. The parent's upsert and row lock serialize child provisioning across
// gateways, and the transaction keeps a partially provisioned pool invisible.
func (r *openAIOAuthDailySessionRepository) GetOrCreateOAuthDailySessionPoolForOS(ctx context.Context, accountID int64, defaultOS string, now time.Time) (service.OAuthDailySessionPool, error) {
	if r == nil || r.client == nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("nil OpenAI OAuth daily-session repository")
	}
	defaultOS = service.NormalizeOpenAIOSFamily(defaultOS)
	if defaultOS == "" {
		return service.OAuthDailySessionPool{}, fmt.Errorf("invalid OAuth daily-session default OS")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("begin OAuth daily OS roots: %w", err)
	}
	defer tx.Rollback()
	txRepo := &openAIOAuthDailySessionRepository{client: tx.Client()}
	pool, err := txRepo.GetOrCreateOAuthDailySessionPool(ctx, accountID, now)
	if err != nil {
		return service.OAuthDailySessionPool{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM openai_oauth_daily_session_pools
		WHERE account_id=$1 AND business_date=$2 FOR UPDATE`, pool.AccountID, pool.BusinessDate)
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("lock OAuth daily session pool: %w", err)
	}
	var poolID int64
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err == nil {
			err = fmt.Errorf("pool disappeared during provisioning")
		}
		return service.OAuthDailySessionPool{}, fmt.Errorf("lock OAuth daily session pool: %w", err)
	}
	err = rows.Scan(&poolID)
	rows.Close()
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("scan OAuth daily session pool id: %w", err)
	}
	stored, err := loadOAuthDailyOSRoots(ctx, tx, []int64{poolID})
	if err != nil {
		return service.OAuthDailySessionPool{}, err
	}
	attachOAuthDailyOSRoots(&pool, stored[poolID])
	if pool.DefaultOS != "" {
		// The existing legacy-sync association wins even if a later caller
		// supplies a different default. Published roots are never reassigned.
		defaultOS = pool.DefaultOS
	}
	if pool.OSRoots == nil {
		pool.OSRoots = make(map[string]service.OAuthDailyOSRoots, service.OAuthDailyStreamSessionCount)
	}
	for slot, osFamily := range service.OpenAIOAuthOSFamilies() {
		if _, exists := pool.OSRoots[osFamily]; exists {
			continue
		}
		root := service.OAuthDailyOSRoots{StreamSessionID: pool.StreamSessionIDs[slot], SyncSessionID: pool.SyncSessionID}
		if osFamily != defaultOS {
			root.SyncSessionID, err = newUUIDv7()
			if err != nil {
				return service.OAuthDailySessionPool{}, fmt.Errorf("generate OAuth daily OS sync root: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO openai_oauth_daily_os_roots
			(pool_id,os_family,stream_session_id,sync_session_id) VALUES ($1,$2,$3,$4)`,
			poolID, osFamily, root.StreamSessionID, root.SyncSessionID)
		if err != nil {
			return service.OAuthDailySessionPool{}, fmt.Errorf("create OAuth daily %s roots: %w", osFamily, err)
		}
		pool.OSRoots[osFamily] = root
	}
	attachOAuthDailyOSRoots(&pool, pool.OSRoots)
	if pool.DefaultOS == "" {
		return service.OAuthDailySessionPool{}, fmt.Errorf("OAuth daily OS roots have no legacy synchronous root association")
	}
	if err := tx.Commit(); err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("commit OAuth daily OS roots: %w", err)
	}
	return pool, nil
}

func loadOAuthDailyOSRoots(ctx context.Context, db sqlExecutor, poolIDs []int64) (map[int64]map[string]service.OAuthDailyOSRoots, error) {
	result := make(map[int64]map[string]service.OAuthDailyOSRoots, len(poolIDs))
	if len(poolIDs) == 0 {
		return result, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT pool_id,os_family,stream_session_id,sync_session_id
		FROM openai_oauth_daily_os_roots WHERE pool_id=ANY($1)`, pq.Array(poolIDs))
	if err != nil {
		return nil, fmt.Errorf("read OAuth daily OS roots: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var poolID int64
		var osFamily string
		var root service.OAuthDailyOSRoots
		if err := rows.Scan(&poolID, &osFamily, &root.StreamSessionID, &root.SyncSessionID); err != nil {
			return nil, fmt.Errorf("scan OAuth daily OS roots: %w", err)
		}
		if result[poolID] == nil {
			result[poolID] = make(map[string]service.OAuthDailyOSRoots, service.OAuthDailyStreamSessionCount)
		}
		result[poolID][osFamily] = root
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate OAuth daily OS roots: %w", err)
	}
	return result, nil
}

func attachOAuthDailyOSRoots(pool *service.OAuthDailySessionPool, roots map[string]service.OAuthDailyOSRoots) {
	pool.OSRoots = roots
	pool.DefaultOS = ""
	for osFamily, root := range roots {
		if root.SyncSessionID == pool.SyncSessionID {
			pool.DefaultOS = osFamily
			break
		}
	}
}

func (r *openAIOAuthDailySessionRepository) GetOrCreateOAuthDailySessionAffinity(ctx context.Context, accountID, apiKeyID int64, logicalKey string, now time.Time) (service.OAuthDailySessionAffinity, error) {
	if r == nil || r.client == nil {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("nil OpenAI OAuth daily-session repository")
	}
	if logicalKey == "" {
		logicalKey = "default"
	}
	owner, err := r.ownerAccountID(ctx, accountID)
	if err != nil {
		return service.OAuthDailySessionAffinity{}, err
	}
	pool, err := r.GetOrCreateOAuthDailySessionPool(ctx, owner, now)
	if err != nil {
		return service.OAuthDailySessionAffinity{}, err
	}
	slot, err := cryptorand.Int(cryptorand.Reader, big.NewInt(service.OAuthDailyStreamSessionCount))
	if err != nil {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("choose OAuth daily session slot: %w", err)
	}
	idx := int(slot.Int64())
	rows, err := r.client.QueryContext(ctx, `INSERT INTO openai_oauth_daily_session_affinities
		(account_id,api_key_id,logical_session_key,business_date,generation,slot_index,stream_session_id,last_seen_at,active,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,NOW(),NOW())
		ON CONFLICT (account_id,api_key_id,logical_session_key,business_date) DO UPDATE SET
			business_date=EXCLUDED.business_date,
			generation=CASE WHEN openai_oauth_daily_session_affinities.business_date=EXCLUDED.business_date THEN openai_oauth_daily_session_affinities.generation ELSE EXCLUDED.generation END,
			slot_index=CASE WHEN openai_oauth_daily_session_affinities.business_date=EXCLUDED.business_date THEN openai_oauth_daily_session_affinities.slot_index ELSE EXCLUDED.slot_index END,
			stream_session_id=CASE WHEN openai_oauth_daily_session_affinities.business_date=EXCLUDED.business_date THEN openai_oauth_daily_session_affinities.stream_session_id ELSE EXCLUDED.stream_session_id END,
			last_seen_at=EXCLUDED.last_seen_at, active=TRUE, updated_at=NOW()
		RETURNING business_date,generation,slot_index,stream_session_id`, owner, apiKeyID, logicalKey, pool.BusinessDate, pool.Generation, idx, pool.StreamSessionIDs[idx], now)
	if err != nil {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("persist OAuth daily session affinity for account %d: %w", owner, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return service.OAuthDailySessionAffinity{}, fmt.Errorf("read OAuth daily session affinity for account %d: %w", owner, err)
		}
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("read OAuth daily session affinity for account %d: query returned no rows", owner)
	}
	result := service.OAuthDailySessionAffinity{AccountID: owner, APIKeyID: apiKeyID, LogicalSessionKey: logicalKey}
	if err := rows.Scan(&result.BusinessDate, &result.Generation, &result.SlotIndex, &result.StreamSessionID); err != nil {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("scan OAuth daily session affinity for account %d: %w", owner, err)
	}
	return result, nil
}

func (r *openAIOAuthDailySessionRepository) ListOAuthDailySessionPools(ctx context.Context, accountIDs []int64, now time.Time) (map[int64]service.OAuthDailySessionPool, error) {
	result := make(map[int64]service.OAuthDailySessionPool)
	if r == nil || r.client == nil || len(accountIDs) == 0 {
		return result, nil
	}
	date := service.OAuthDailyBusinessDate(now)
	rows, err := r.client.OpenAIOAuthDailySessionPool.Query().Where(openaioauthdailysessionpool.AccountIDIn(accountIDs...), openaioauthdailysessionpool.BusinessDateEQ(date)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list OAuth daily session pools: %w", err)
	}
	poolIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		poolIDs = append(poolIDs, row.ID)
	}
	osRoots, err := loadOAuthDailyOSRoots(ctx, r.client, poolIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		pool := service.OAuthDailySessionPool{AccountID: row.AccountID, BusinessDate: row.BusinessDate, Generation: row.Generation, StreamSessionIDs: [3]string{row.StreamSession0, row.StreamSession1, row.StreamSession2}, SyncSessionID: row.SyncSession}
		attachOAuthDailyOSRoots(&pool, osRoots[row.ID])
		result[row.AccountID] = pool
	}
	return result, nil
}

func (r *openAIOAuthDailySessionRepository) ReleaseOAuthDailySessionGeneration(ctx context.Context, accountID int64, generation string) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("nil OpenAI OAuth daily-session repository")
	}
	owner, err := r.ownerAccountID(ctx, accountID)
	if err != nil {
		return err
	}
	_, err = r.client.OpenAIOAuthDailySessionPool.Update().Where(openaioauthdailysessionpool.AccountIDEQ(owner), openaioauthdailysessionpool.GenerationEQ(generation)).SetActiveStreams(0).Save(ctx)
	return err
}

func (r *openAIOAuthDailySessionRepository) CleanupOAuthDailySessionGenerations(ctx context.Context, before time.Time) (int, error) {
	if r == nil || r.client == nil {
		return 0, fmt.Errorf("nil OpenAI OAuth daily-session repository")
	}
	// Keep any generation touched after the cutoff; callers should release active
	// streams before invoking cleanup.
	return r.client.OpenAIOAuthDailySessionPool.Delete().Where(openaioauthdailysessionpool.UpdatedAtLT(before), openaioauthdailysessionpool.ActiveStreamsEQ(0)).Exec(ctx)
}
