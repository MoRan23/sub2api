package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Retrying a failed commit only retries database work. Each attempt receives a
// fresh result so rolled-back actions cannot leak into the eventual audit row.
func (r *attributionRepository) Finish(ctx context.Context, j *service.AttributionJob) error {
	if j == nil {
		return nil
	}
	result, err := json.Marshal(j.Result)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		candidate := *j
		candidate.Result = service.AttributionResult{}
		if err = json.Unmarshal(result, &candidate.Result); err != nil {
			return err
		}
		err = r.finishAttempt(ctx, &candidate)
		if err == nil {
			*j = candidate
			return nil
		}
		var pg *pq.Error
		if attempt >= 3 || !errors.As(err, &pg) || (pg.Code != "40001" && pg.Code != "40P01" && pg.Code != "55P03") {
			return err
		}
		timer := time.NewTimer(time.Duration(25*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *attributionRepository) validateAttributionConfigGroups(ctx context.Context, tx *sql.Tx, c service.AttributionConfig) error {
	ids := c.Default.ActionGroups()
	for _, p := range c.Groups {
		ids = append(ids, p.ActionGroups()...)
	}
	if err := lockAccountGroupRows(ctx, tx, ids); err != nil {
		return err
	}
	reason, err := r.attributionGroupsIssue(ctx, tx, ids, nil)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("%w: %s", service.ErrAttributionInvalid, reason)
	}
	return nil
}

func (r *attributionRepository) attributionTargetGroupIssue(ctx context.Context, tx *sql.Tx, a *service.Account, j *service.AttributionJob) (string, error) {
	if j.Snapshot.Version < 5 || j.Snapshot.EffectiveResultMode() != service.AttributionResultModeReplace {
		return "", nil
	}
	groups := j.Snapshot.Policy.HighGroupIDs
	if j.Status == "mismatch" {
		groups = j.Snapshot.Policy.LowGroupIDs
	}
	if groups == nil {
		return "", nil
	}
	if len(*groups) == 0 {
		return "target_groups_unavailable", nil
	}
	return r.attributionGroupsIssue(ctx, tx, *groups, a)
}

// The caller holds sorted group locks, including soft-deleted targets. Missing
// or newly forbidden targets are an unapplied result, not a failed inference.
func (r *attributionRepository) attributionGroupsIssue(ctx context.Context, tx *sql.Tx, ids []int64, a *service.Account) (string, error) {
	ids = uniquePositiveInt64s(ids)
	if len(ids) == 0 {
		return "", nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,platform,require_oauth_only,deleted_at IS NOT NULL FROM groups WHERE id=ANY($1) ORDER BY id`, pq.Array(ids))
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	reason := ""
	for rows.Next() {
		var id int64
		var platform string
		var oauthOnly, deleted bool
		if err = rows.Scan(&id, &platform, &oauthOnly, &deleted); err != nil {
			return "", err
		}
		count++
		if deleted {
			reason = "target_groups_unavailable"
			continue
		}
		if reason == "target_groups_unavailable" {
			continue
		}
		if platform != service.PlatformOpenAI && platform != service.PlatformComposite ||
			r.cfg != nil && r.cfg.RunMode == config.RunModeSimple && platform == service.PlatformComposite ||
			a != nil && a.Type == service.AccountTypeAPIKey && oauthOnly {
			reason = "target_groups_incompatible"
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if count != len(ids) {
		return "target_groups_unavailable", nil
	}
	return reason, nil
}

func sameAttributionGroupBindings(current []service.AccountGroup, ids []int64) bool {
	if len(current) != len(ids) {
		return false
	}
	for i, id := range ids {
		if current[i].GroupID != id || current[i].Priority != i+1 {
			return false
		}
	}
	return true
}

func replaceAttributionGroups(ctx context.Context, tx *sql.Tx, accountID int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) SELECT $1,id,ord FROM unnest($2::bigint[]) WITH ORDINALITY AS target(id,ord)`, accountID, pq.Array(ids)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET updated_at=NOW() WHERE id=$1`, accountID)
	return err
}
