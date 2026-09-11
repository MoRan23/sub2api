package repository

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/openaioauthdailysessionaffinity"
	"github.com/Wei-Shaw/sub2api/ent/openaioauthdailysessionpool"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type openAIOAuthDailySessionRepository struct{ client *ent.Client }

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
	_, err = r.client.ExecContext(ctx, `
		INSERT INTO openai_oauth_daily_session_pools
		(account_id,business_date,generation,stream_session_0,stream_session_1,stream_session_2,sync_session,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NOW(),NOW())
		ON CONFLICT (account_id,business_date) DO NOTHING`, owner, date, generation, roots[0], roots[1], roots[2], roots[3])
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("create OAuth daily session pool for account %d date %s: %w", owner, date, err)
	}
	row, err := r.client.OpenAIOAuthDailySessionPool.Query().Where(
		openaioauthdailysessionpool.AccountIDEQ(owner), openaioauthdailysessionpool.BusinessDateEQ(date),
	).Only(ctx)
	if err != nil {
		return service.OAuthDailySessionPool{}, fmt.Errorf("read OAuth daily session pool for account %d date %s: %w", owner, date, err)
	}
	return service.OAuthDailySessionPool{AccountID: owner, BusinessDate: row.BusinessDate, Generation: row.Generation,
		StreamSessionIDs: [3]string{row.StreamSession0, row.StreamSession1, row.StreamSession2}, SyncSessionID: row.SyncSession}, nil
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
	existing, qerr := r.client.OpenAIOAuthDailySessionAffinity.Query().Where(
		openaioauthdailysessionaffinity.AccountIDEQ(owner), openaioauthdailysessionaffinity.APIKeyIDEQ(apiKeyID), openaioauthdailysessionaffinity.LogicalSessionKeyEQ(logicalKey),
	).Only(ctx)
	if qerr != nil && !ent.IsNotFound(qerr) {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("read OAuth daily session affinity for account %d: %w", owner, qerr)
	}
	if qerr == nil && existing.BusinessDate == pool.BusinessDate {
		_, _ = existing.Update().SetLastSeenAt(now).SetActive(true).Save(ctx)
		return service.OAuthDailySessionAffinity{AccountID: owner, APIKeyID: apiKeyID, LogicalSessionKey: logicalKey, BusinessDate: existing.BusinessDate, Generation: existing.Generation, SlotIndex: existing.SlotIndex, StreamSessionID: existing.StreamSessionID}, nil
	}
	slot, err := cryptorand.Int(cryptorand.Reader, big.NewInt(service.OAuthDailyStreamSessionCount))
	if err != nil {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("choose OAuth daily session slot: %w", err)
	}
	idx := int(slot.Int64())
	if qerr == nil {
		_, err = r.client.ExecContext(ctx, `UPDATE openai_oauth_daily_session_affinities
			SET business_date=$1,generation=$2,slot_index=$3,stream_session_id=$4,last_seen_at=$5,active=TRUE,updated_at=NOW()
			WHERE account_id=$6 AND api_key_id=$7 AND logical_session_key=$8 AND business_date<>$1`,
			pool.BusinessDate, pool.Generation, idx, pool.StreamSessionIDs[idx], now, owner, apiKeyID, logicalKey)
		if err == nil {
			existing, err = r.client.OpenAIOAuthDailySessionAffinity.Query().Where(openaioauthdailysessionaffinity.AccountIDEQ(owner), openaioauthdailysessionaffinity.APIKeyIDEQ(apiKeyID), openaioauthdailysessionaffinity.LogicalSessionKeyEQ(logicalKey)).Only(ctx)
		}
	} else {
		_, err = r.client.ExecContext(ctx, `INSERT INTO openai_oauth_daily_session_affinities
			(account_id,api_key_id,logical_session_key,business_date,generation,slot_index,stream_session_id,last_seen_at,active,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,NOW(),NOW())
			ON CONFLICT (account_id,api_key_id,logical_session_key) DO NOTHING`, owner, apiKeyID, logicalKey, pool.BusinessDate, pool.Generation, idx, pool.StreamSessionIDs[idx], now)
		if err == nil {
			existing, err = r.client.OpenAIOAuthDailySessionAffinity.Query().Where(openaioauthdailysessionaffinity.AccountIDEQ(owner), openaioauthdailysessionaffinity.APIKeyIDEQ(apiKeyID), openaioauthdailysessionaffinity.LogicalSessionKeyEQ(logicalKey)).Only(ctx)
		}
	}
	if err != nil {
		return service.OAuthDailySessionAffinity{}, fmt.Errorf("persist OAuth daily session affinity for account %d: %w", owner, err)
	}
	return service.OAuthDailySessionAffinity{AccountID: owner, APIKeyID: apiKeyID, LogicalSessionKey: logicalKey, BusinessDate: existing.BusinessDate, Generation: existing.Generation, SlotIndex: existing.SlotIndex, StreamSessionID: existing.StreamSessionID}, nil
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
