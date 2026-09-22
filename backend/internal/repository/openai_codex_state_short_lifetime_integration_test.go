//go:build integration

package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexStateShortLifetimeMigrationPreservesHistoryAndLimits(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE openai_codex_state
		(LIKE public.openai_codex_state INCLUDING DEFAULTS);
		ALTER TABLE openai_codex_state DROP COLUMN authorization_generation, ADD COLUMN os_family TEXT`)
	require.NoError(t, err)
	var now time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT NOW()`).Scan(&now))
	for _, tc := range []struct {
		model, demand, status, reason string
		collectionReason              string
		age, expiry, retry            time.Duration
		paused, collecting            bool
	}{
		{model: "shortened_expired", age: 5 * time.Minute, expiry: 55 * time.Minute},
		{model: "earlier_expiry", expiry: 10 * time.Second},
		{model: "retained_proxy_cache", expiry: 10 * time.Second, collectionReason: "collector_proxy_changed"},
		{model: "retained_proxy_demand", expiry: 10 * time.Second, demand: "extended_shape", collectionReason: "collector_proxy_changed"},
		{model: "fresh", expiry: time.Hour},
		{model: "shape_retry", demand: "extended_shape", status: "backoff", reason: "no_target_state", retry: 30 * time.Second},
		{model: "ordinary_failure", demand: "extended_shape", status: "backoff", reason: "collection_timeout", retry: 50 * time.Second},
		{model: "collecting", demand: "extended_shape", status: "collecting", retry: 50 * time.Second, collecting: true},
		{model: "collecting_after_old_limit", demand: "extended_shape", status: "collecting", reason: "collector_rate_limited", retry: 50 * time.Second, collecting: true},
		{model: "paused", demand: "extended_shape", status: "paused", reason: "collector_auth_rejected", retry: time.Minute, paused: true},
		{model: "rate_limited", demand: "extended_shape", status: "backoff", reason: "collector_rate_limited", retry: 2 * time.Minute},
		{model: "legacy_cooldown", demand: "extended_shape", status: "backoff", reason: "account_cooldown", retry: 3 * time.Minute},
	} {
		var expiry, next, attempt any
		token, shape := "", "extended"
		if tc.expiry != 0 {
			expiry, token, shape = now.Add(tc.expiry), "synthetic-encrypted-history", "target"
		}
		if tc.retry != 0 {
			next = now.Add(tc.retry)
		}
		if tc.collecting {
			attempt = "00000000-0000-4000-8000-000000000001"
		}
		collectionReason := tc.reason
		if tc.collectionReason != "" {
			collectionReason = tc.collectionReason
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state
			(owner_account_id,os_family,model,generation,version,encrypted_token,issued_at,expires_at,
			shape,last_business_at,last_collected_at,next_collect_at,collector_paused,last_error,
			demand_reason,demand_at,collection_status,collection_reason,collector_attempt_id,history_proof_observed_at)
			VALUES(1,'windows',$1,'unchanged-generation',7,$2,$3,$4,$5,$6,$6,$7,$8,$9,$10,$6,$11,$13,$12,$6)`,
			tc.model, token, now.Add(-tc.age), expiry, shape, now, next, tc.paused, tc.reason, tc.demand, tc.status, attempt, collectionReason)
		require.NoError(t, err)
	}
	migration, err := os.ReadFile("../../migrations/253_openai_codex_state_short_lifetime.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for _, tc := range []struct {
		model, demand, status string
		expiry, retry         time.Duration
		paused                bool
	}{
		{model: "shortened_expired", expiry: -time.Minute, demand: "expiring", status: "pending"},
		{model: "earlier_expiry", expiry: 10 * time.Second, demand: "expiring", status: "pending"},
		{model: "retained_proxy_cache", expiry: 10 * time.Second, status: "idle"},
		{model: "retained_proxy_demand", expiry: 10 * time.Second, demand: "extended_shape", status: "idle"},
		{model: "fresh", expiry: 240 * time.Second, status: "idle"},
		{model: "shape_retry", demand: "extended_shape", status: "backoff", retry: 5 * time.Second},
		{model: "ordinary_failure", demand: "extended_shape", status: "pending"},
		{model: "collecting", demand: "extended_shape", status: "pending"},
		{model: "collecting_after_old_limit", demand: "extended_shape", status: "pending"},
		{model: "paused", demand: "extended_shape", status: "paused", retry: time.Minute, paused: true},
		{model: "rate_limited", demand: "extended_shape", status: "backoff", retry: 2 * time.Minute},
		{model: "legacy_cooldown", demand: "extended_shape", status: "backoff", retry: 3 * time.Minute},
	} {
		t.Run(tc.model, func(t *testing.T) {
			var expiry, next sql.NullTime
			var attempt sql.NullString
			var token, demand, status, generation, collectionReason string
			var version int64
			var paused bool
			var history time.Time
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT encrypted_token,expires_at,next_collect_at,demand_reason,
				collection_status,collector_paused,version,collector_attempt_id,history_proof_observed_at,generation,collection_reason
				FROM openai_codex_state WHERE model=$1`, tc.model).
				Scan(&token, &expiry, &next, &demand, &status, &paused, &version, &attempt, &history, &generation, &collectionReason))
			require.EqualValues(t, 8, version, "pre-migration collector snapshots must fail CAS")
			require.False(t, attempt.Valid)
			require.Equal(t, "unchanged-generation", generation)
			require.Equal(t, now, history, "safe business history must survive the policy change")
			require.Equal(t, tc.demand, demand)
			require.Equal(t, tc.status, status)
			if tc.model == "retained_proxy_cache" || tc.model == "retained_proxy_demand" {
				require.Equal(t, "collector_proxy_changed", collectionReason)
			}
			require.Equal(t, tc.paused, paused)
			if tc.expiry != 0 {
				require.Equal(t, "synthetic-encrypted-history", token)
				require.Equal(t, now.Add(tc.expiry), expiry.Time)
			} else {
				require.False(t, expiry.Valid)
			}
			if tc.retry == 0 {
				require.False(t, next.Valid)
			} else {
				require.Equal(t, now.Add(tc.retry), next.Time)
			}
		})
	}
}
