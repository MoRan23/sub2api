package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// EnqueueNewAccounts consumes durable account-creation events. Both queues and
// the marker commit together, so restarts/replicas cannot launch a second test.
// No model request (including catalog lookup) is made while holding DB locks;
// the pelican worker performs its existing catalog and authorization checks.
func (r *attributionRepository) EnqueueNewAccounts(ctx context.Context) error {
	tx, err := r.transaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Always take attribution before candy; neither existing queue reverses this.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(261,321)`); err != nil {
		return err
	}
	c, err := attributionConfig(ctx, tx)
	if err != nil {
		return err
	}
	type pending struct {
		id                             int64
		attribution, pelican           bool
		attributionModel, pelicanModel string
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_id,attribution,pelican,model,pelican_model FROM account_initial_tests WHERE processed_at IS NULL ORDER BY account_id LIMIT 50`)
	if err != nil {
		return err
	}
	var accounts []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.attribution, &p.pelican, &p.attributionModel, &p.pelicanModel); err != nil {
			_ = rows.Close()
			return err
		}
		accounts = append(accounts, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range accounts {
		// Eligibility does not require ModelTrace to be enabled for pelican.
		eligibility := c
		eligibility.Enabled = true
		a, _, skip, err := attributionSnapshot(ctx, tx, p.id, eligibility, false)
		if err != nil {
			return err
		}
		if a != nil && skip == "" {
			var hasAttribution, hasPelican bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM model_attribution_jobs WHERE account_id=$1), EXISTS(SELECT 1 FROM account_candy_test_items WHERE account_id=$1 AND prompt_version=$2)`, p.id, service.CandyTestPromptVersion).Scan(&hasAttribution, &hasPelican); err != nil {
				return err
			}
			if p.attribution && c.Enabled && c.NewAccountTests.Attribution && !hasAttribution && !service.AccountDiagnosticRateLimited(a, p.attributionModel, time.Now()) {
				if _, err = enqueueAttribution(ctx, tx, p.id, c, "initial", p.attributionModel); err != nil {
					return err
				}
			}
			if p.pelican && c.NewAccountTests.Pelican && !hasPelican && !service.AccountDiagnosticRateLimited(a, p.pelicanModel, time.Now()) {
				request := &service.CandyTestCreateRequest{AccountIDs: []int64{p.id}, Model: p.pelicanModel, IdempotencyKey: fmt.Sprintf("new-account-pelican:%d", p.id)}
				if _, err = createCandyBatch(ctx, tx, request, []*service.CandyTestItem{{AccountID: p.id, AccountName: a.Name, Status: "queued"}}); err != nil {
					return err
				}
			}
		}
		// Disabling the initial attribution test still allows the normal first
		// periodic cycle ten minutes later; never enqueue it in this same tick.
		if _, err = tx.ExecContext(ctx, `INSERT INTO model_attribution_state(account_id,next_due_at) VALUES($1,NOW()+interval '10 minutes') ON CONFLICT DO NOTHING`, p.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE account_initial_tests SET processed_at=NOW() WHERE account_id=$1`, p.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
