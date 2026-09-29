package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type accountCandyTestRepository struct{ db *sql.DB }

func NewAccountCandyTestRepository(db *sql.DB) service.CandyTestRepository {
	return &accountCandyTestRepository{db: db}
}

// All queue transitions share a transaction-level lock. Heartbeats only extend a
// currently owned lease and never allocate capacity. No network I/O holds this lock.
func (r *accountCandyTestRepository) transaction(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(261, 321)`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (r *accountCandyTestRepository) Create(ctx context.Context, request *service.CandyTestCreateRequest, items []*service.CandyTestItem) (*service.CandyTestBatch, error) {
	tx, err := r.transaction(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := json.Marshal(struct {
		AccountIDs []int64 `json:"account_ids"`
		Model      string  `json:"model"`
		Effort     string  `json:"reasoning_effort"`
	}{request.AccountIDs, request.Model, request.ReasoningEffort})
	if err != nil {
		return nil, err
	}
	var existing string
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT id::text, request_snapshot = $2::jsonb FROM account_candy_test_batches WHERE idempotency_key=$1`, request.IdempotencyKey, snapshot).Scan(&existing, &matches)
	if err == nil {
		if !matches {
			return nil, service.ErrCandyTestIdempotencyConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return r.GetBatch(ctx, existing, 1, 50)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	id := uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO account_candy_test_batches(id,idempotency_key,request_snapshot,model,reasoning_effort,prompt_version,total) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, request.IdempotencyKey, snapshot, request.Model, request.ReasoningEffort, service.CandyTestPromptVersion, len(items))
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		_, err = tx.ExecContext(ctx, `INSERT INTO account_candy_test_items(batch_id,account_id,account_name,model,reasoning_effort,prompt_version,status,failure_code,finished_at) VALUES($1,$2,$3,$4,$5,$6,$7::varchar,$8,CASE WHEN $7::varchar='skipped' THEN NOW() ELSE NULL END)`, id, item.AccountID, item.AccountName, request.Model, request.ReasoningEffort, service.CandyTestPromptVersion, item.Status, item.FailureCode)
		if err != nil {
			return nil, err
		}
	}
	if err = maintainCandyBatches(ctx, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetBatch(ctx, id, 1, 50)
}

const candyItemColumns = `id,batch_id::text,account_id,account_name,model,reasoning_effort,prompt_version,status,answers,response_text,failure_code,execution,created_at,started_at,finished_at,cancel_requested,COALESCE(claim_id::text,''),lease_until`

func scanCandyItem(row scannable) (*service.CandyTestItem, error) {
	i := &service.CandyTestItem{}
	var answers, execution []byte
	if err := row.Scan(&i.ID, &i.BatchID, &i.AccountID, &i.AccountName, &i.Model, &i.ReasoningEffort, &i.PromptVersion, &i.Status, &answers, &i.ResponseText, &i.FailureCode, &execution, &i.CreatedAt, &i.StartedAt, &i.FinishedAt, &i.CancelRequested, &i.ClaimID, &i.LeaseUntil); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(answers, &i.Answers); err != nil {
		return nil, err
	}
	if len(execution) > 0 {
		if err := json.Unmarshal(execution, &i.Execution); err != nil {
			return nil, err
		}
	}
	return i, nil
}

func scanCandyItems(rows *sql.Rows) ([]*service.CandyTestItem, error) {
	items := make([]*service.CandyTestItem, 0)
	for rows.Next() {
		i, err := scanCandyItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (r *accountCandyTestRepository) GetBatch(ctx context.Context, id string, page, size int) (*service.CandyTestBatch, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, service.ErrCandyTestNotFound
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	b := &service.CandyTestBatch{Page: page, PageSize: size}
	var counts []byte
	err := r.db.QueryRowContext(ctx, `SELECT id::text,model,reasoning_effort,prompt_version,created_at,finished_at,total,counts FROM account_candy_test_batches WHERE id=$1`, id).Scan(&b.ID, &b.Model, &b.ReasoningEffort, &b.PromptVersion, &b.CreatedAt, &b.FinishedAt, &b.Total, &counts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCandyTestNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(counts, &b.Counts); err != nil {
		return nil, err
	}
	if err = r.db.QueryRowContext(ctx, `SELECT count(*) FROM account_candy_test_items WHERE batch_id=$1`, id).Scan(&b.RetainedTotal); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+candyItemColumns+` FROM account_candy_test_items WHERE batch_id=$1 ORDER BY id LIMIT $2 OFFSET $3`, id, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	b.Items, err = scanCandyItems(rows)
	return b, err
}

func (r *accountCandyTestRepository) Claim(ctx context.Context) (*service.CandyTestItem, error) {
	tx, err := r.transaction(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE account_candy_test_items SET status=CASE WHEN cancel_requested THEN 'cancelled' ELSE 'failed' END,failure_code=CASE WHEN cancel_requested THEN 'cancelled' WHEN started_at + INTERVAL '30 minutes' <= NOW() THEN 'timeout' ELSE 'execution_interrupted' END,finished_at=NOW(),claim_id=NULL,lease_until=NULL WHERE status='running' AND (lease_until<=NOW() OR started_at+INTERVAL '30 minutes'<=NOW())`)
	if err != nil {
		return nil, err
	}
	if err = maintainCandyBatches(ctx, tx); err != nil {
		return nil, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM account_candy_test_items WHERE status='running'`).Scan(&count); err != nil {
		return nil, err
	}
	if count >= service.CandyTestMaxConcurrent {
		return nil, tx.Commit()
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT i.id FROM account_candy_test_items i WHERE i.status='queued' AND NOT EXISTS(SELECT 1 FROM account_candy_test_items active WHERE active.account_id=i.account_id AND active.status='running') ORDER BY i.id LIMIT 1 FOR UPDATE OF i SKIP LOCKED`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	item, err := scanCandyItem(tx.QueryRowContext(ctx, `UPDATE account_candy_test_items SET status='running',claim_id=$2,lease_until=NOW()+INTERVAL '30 seconds',started_at=NOW() WHERE id=$1 RETURNING `+candyItemColumns, id, uuid.NewString()))
	if err != nil {
		return nil, err
	}
	if err = maintainCandyBatches(ctx, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *accountCandyTestRepository) Heartbeat(ctx context.Context, id int64, claim string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE account_candy_test_items SET lease_until=NOW()+INTERVAL '30 seconds' WHERE id=$1 AND claim_id=$2 AND status='running' AND NOT cancel_requested AND lease_until>NOW() AND started_at+INTERVAL '30 minutes'>NOW()`, id, claim)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *accountCandyTestRepository) Complete(ctx context.Context, item *service.CandyTestItem) (bool, error) {
	switch item.Status {
	case "normal", "abnormal", "failed", "cancelled":
	default:
		return false, service.ErrCandyTestInvalidRequest
	}
	if len(item.ResponseText) > service.CandyTestMaxResponseBytes {
		return false, service.ErrCandyTestInvalidRequest
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	answers, err := json.Marshal(item.Answers)
	if err != nil {
		return false, err
	}
	execution, err := json.Marshal(item.Execution)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_candy_test_items SET status=CASE WHEN cancel_requested THEN 'cancelled' WHEN started_at+INTERVAL '30 minutes'<=NOW() THEN 'failed' ELSE $3 END, failure_code=CASE WHEN cancel_requested THEN 'cancelled' WHEN started_at+INTERVAL '30 minutes'<=NOW() THEN 'timeout' ELSE $4 END,answers=CASE WHEN cancel_requested OR started_at+INTERVAL '30 minutes'<=NOW() THEN '{}'::jsonb ELSE $5::jsonb END,response_text=$6,execution=$7,finished_at=NOW(),claim_id=NULL,lease_until=NULL WHERE id=$1 AND claim_id=$2 AND status='running' AND lease_until>NOW()`, item.ID, item.ClaimID, item.Status, item.FailureCode, answers, item.ResponseText, execution)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = maintainCandyBatches(ctx, tx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

func (r *accountCandyTestRepository) Cancel(ctx context.Context, id string, itemIDs []int64) error {
	if _, err := uuid.Parse(id); err != nil {
		return service.ErrCandyTestNotFound
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_candy_test_batches WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return service.ErrCandyTestNotFound
	}
	args := []any{id}
	filter := ""
	if len(itemIDs) > 0 {
		filter = " AND id IN (" + candyPlaceholders(&args, itemIDs) + ")"
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_candy_test_items SET cancel_requested=TRUE,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,failure_code=CASE WHEN status='queued' THEN 'cancelled' ELSE failure_code END,finished_at=CASE WHEN status='queued' THEN NOW() ELSE finished_at END WHERE batch_id=$1 AND status IN ('queued','running')`+filter, args...)
	if err != nil {
		return err
	}
	if err = maintainCandyBatches(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *accountCandyTestRepository) History(ctx context.Context, accountID int64) ([]*service.CandyTestItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+candyItemColumns+` FROM account_candy_test_items WHERE account_id=$1 AND status NOT IN ('queued','running') ORDER BY finished_at DESC,id DESC LIMIT 5`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanCandyItems(rows)
}

func (r *accountCandyTestRepository) Summaries(ctx context.Context, ids []int64) (map[int64]*service.CandyTestSummary, error) {
	result := make(map[int64]*service.CandyTestSummary)
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(ids))
	placeholders := candyPlaceholders(&args, ids)
	// Rank before selecting the text column: account lists never load raw answers.
	rows, err := r.db.QueryContext(ctx, `SELECT id,batch_id::text,account_id,account_name,model,reasoning_effort,prompt_version,status,'{}'::jsonb,''::text,failure_code,execution,created_at,started_at,finished_at,cancel_requested,''::text,NULL::timestamptz FROM (SELECT i.*,ROW_NUMBER() OVER(PARTITION BY account_id,(status IN ('queued','running')) ORDER BY (status='running') DESC,COALESCE(finished_at,created_at) DESC,id DESC) AS rn FROM account_candy_test_items i WHERE account_id IN (`+placeholders+`)) ranked WHERE rn=1`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		item, err := scanCandyItem(rows)
		if err != nil {
			return nil, err
		}
		summary := result[item.AccountID]
		if summary == nil {
			summary = &service.CandyTestSummary{}
			result[item.AccountID] = summary
		}
		if item.Status == "queued" || item.Status == "running" {
			summary.Active = item
		} else {
			summary.Latest = item
		}
	}
	return result, rows.Err()
}

func candyPlaceholders(args *[]any, ids []int64) string {
	p := make([]string, len(ids))
	for i, id := range ids {
		*args = append(*args, id)
		p[i] = fmt.Sprintf("$%d", len(*args))
	}
	return strings.Join(p, ",")
}

// Final batch counts are frozen before history pruning. Running batches retain
// all their items so a result cannot disappear while another account is pending.
func maintainCandyBatches(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `UPDATE account_candy_test_batches b SET counts=q.counts,finished_at=CASE WHEN q.active=0 THEN NOW() ELSE NULL END FROM (SELECT batch_id,jsonb_object_agg(status,n) AS counts,SUM(CASE WHEN status IN ('queued','running') THEN n ELSE 0 END) AS active FROM (SELECT i.batch_id,i.status,count(*) AS n FROM account_candy_test_items i JOIN account_candy_test_batches pending ON pending.id=i.batch_id WHERE pending.finished_at IS NULL GROUP BY i.batch_id,i.status) grouped GROUP BY batch_id) q WHERE b.id=q.batch_id AND b.finished_at IS NULL AND (b.counts IS DISTINCT FROM q.counts OR q.active=0) RETURNING b.finished_at IS NOT NULL`)
	if err != nil {
		return err
	}
	closedBatch := false
	for rows.Next() {
		var closed bool
		if err = rows.Scan(&closed); err != nil {
			_ = rows.Close()
			return err
		}
		closedBatch = closedBatch || closed
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil || !closedBatch {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM account_candy_test_items WHERE id IN (SELECT id FROM (SELECT i.id,b.finished_at,ROW_NUMBER() OVER(PARTITION BY i.account_id ORDER BY i.finished_at DESC,i.id DESC) AS rn FROM account_candy_test_items i JOIN account_candy_test_batches b ON b.id=i.batch_id WHERE i.status NOT IN ('queued','running')) ranked WHERE rn>5 AND finished_at IS NOT NULL)`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM account_candy_test_batches b WHERE b.finished_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM account_candy_test_items i WHERE i.batch_id=b.id)`)
	return err
}
