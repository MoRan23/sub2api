package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageExportRepository struct{ db *sql.DB }

func NewUsageExportRepository(db *sql.DB) service.UsageExportRepository {
	return &usageExportRepository{db: db}
}

const exportColumns = `id,owner,spec,status,max_id,total_rows,rows_written,size_bytes,file_key,claim_token,attempts,error_code,created_at,expires_at,lease_until`

func scanExport(row interface{ Scan(...any) error }, extra ...any) (*service.UsageExportJob, error) {
	var j service.UsageExportJob
	var owner, spec []byte
	args := []any{&j.ID, &owner, &spec, &j.Status, &j.MaxID, &j.TotalRows, &j.RowsWritten, &j.SizeBytes, &j.FileKey, &j.ClaimToken, &j.Attempts, &j.ErrorCode, &j.CreatedAt, &j.ExpiresAt, &j.LeaseUntil}
	err := row.Scan(append(args, extra...)...)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(owner, &j.Owner); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(spec, &j.Spec); err != nil {
		return nil, err
	}
	return &j, nil
}
func (r *usageExportRepository) CanCreate(ctx context.Context, userID int64) error {
	var blocked bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_export_jobs WHERE owner_id=$1 AND status IN ('queued','running')) OR (SELECT COUNT(*) FROM (SELECT 1 FROM usage_export_jobs WHERE status='queued' LIMIT 20) q)>=20`, userID).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return errors.New("USAGE_EXPORT_QUEUE_FULL")
	}
	return nil
}
func (r *usageExportRepository) Create(ctx context.Context, j *service.UsageExportJob) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(784120303)`); err != nil {
		return err
	}
	var waiting, active int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM (SELECT 1 FROM usage_export_jobs WHERE status='queued' LIMIT 20) q),(SELECT COUNT(*) FROM usage_export_jobs WHERE owner_id=$1 AND status IN ('queued','running'))`, j.Owner.UserID).Scan(&waiting, &active); err != nil {
		return err
	}
	if waiting >= 20 || active > 0 {
		return errors.New("USAGE_EXPORT_QUEUE_FULL")
	}
	owner, _ := json.Marshal(j.Owner)
	spec, _ := json.Marshal(j.Spec)
	err = tx.QueryRowContext(ctx, `INSERT INTO usage_export_jobs(id,owner_id,owner,spec,max_id,total_rows) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at`, j.ID, j.Owner.UserID, owner, spec, j.MaxID, j.TotalRows).Scan(&j.CreatedAt)
	if err != nil {
		return err
	}
	j.Status = "queued"
	return tx.Commit()
}
func (r *usageExportRepository) Get(ctx context.Context, id string) (*service.UsageExportJob, error) {
	return scanExport(r.db.QueryRowContext(ctx, `SELECT `+exportColumns+` FROM usage_export_jobs WHERE id=$1`, id))
}
func (r *usageExportRepository) List(ctx context.Context, owner service.UsageExportOwner) ([]service.UsageExportJob, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+exportColumns+` FROM usage_export_jobs WHERE owner_id=$1 AND owner->>'kind'=$2 AND COALESCE(owner->>'key_id','')=$3 AND COALESCE(owner->>'revision','')=$4 ORDER BY created_at DESC,id DESC LIMIT 50`, owner.UserID, owner.Kind, owner.KeyID, owner.Revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]service.UsageExportJob, 0)
	for rows.Next() {
		j, e := scanExport(rows)
		if e != nil {
			return nil, e
		}
		if j.Owner.Kind == owner.Kind && j.Owner.KeyID == owner.KeyID && j.Owner.Revision == owner.Revision {
			jobs = append(jobs, *j)
		}
	}
	return jobs, rows.Err()
}
func (r *usageExportRepository) Claim(ctx context.Context, token string, reserve int64) (*service.UsageExportJob, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(784120303)`); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE usage_export_jobs SET status='failed',error_code=CASE WHEN status='queued' THEN 'QUEUE_TIMEOUT' ELSE 'WORKER_LOST' END,expires_at=NOW()+INTERVAL '24 hours',lease_until=NULL WHERE (status='queued' AND created_at<NOW()-INTERVAL '1 hour') OR (status='running' AND lease_until<NOW() AND attempts>=2)`)
	if err != nil {
		return nil, err
	}
	var running int
	var allocated int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE lease_until>NOW()),COALESCE(SUM(reserved_bytes),0) FROM usage_export_artifacts`).Scan(&running, &allocated)
	if err != nil {
		return nil, err
	}
	if running >= 2 || allocated+reserve > 2<<30 {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	j, err := scanExport(tx.QueryRowContext(ctx, `UPDATE usage_export_jobs SET status='running',claim_token=$1,attempts=attempts+1,rows_written=0,lease_until=NOW()+INTERVAL '60 seconds',started_at=NOW() WHERE id=(SELECT id FROM usage_export_jobs WHERE status='queued' OR (status='running' AND lease_until<NOW() AND attempts<2) ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+exportColumns, token))
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_export_artifacts(job_id,claim_token,file_key,reserved_bytes,lease_until) VALUES($1,$2,$3,$4,$5)`, j.ID, token, j.ID+"-"+token+".xlsx", reserve, j.LeaseUntil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return j, nil
}
func (r *usageExportRepository) Heartbeat(ctx context.Context, id, token string, count int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `WITH renewed AS (UPDATE usage_export_jobs SET rows_written=$3,lease_until=NOW()+INTERVAL '60 seconds' WHERE id=$1 AND claim_token=$2 AND status='running' AND lease_until>NOW() RETURNING id,claim_token,lease_until) UPDATE usage_export_artifacts a SET lease_until=r.lease_until FROM renewed r WHERE a.job_id=r.id AND a.claim_token=r.claim_token`, id, token, count)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func (r *usageExportRepository) Finish(ctx context.Context, j *service.UsageExportJob) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE usage_export_jobs SET status=$3,rows_written=$4,size_bytes=$5,file_key=$6,error_code=$7,lease_until=NULL,expires_at=NOW()+INTERVAL '24 hours' WHERE id=$1 AND claim_token=$2 AND status='running' AND lease_until>NOW()`, j.ID, j.ClaimToken, j.Status, j.RowsWritten, j.SizeBytes, j.FileKey, j.ErrorCode)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE usage_export_jobs SET lease_until=NULL WHERE id=$1 AND claim_token=$2 AND status='cancelled'`, j.ID, j.ClaimToken); err != nil {
			return false, err
		}
	} else if j.Status == "completed" {
		// Keep the original reservation on failure until unlink succeeds.
		res, err = tx.ExecContext(ctx, `UPDATE usage_export_artifacts SET reserved_bytes=$3,lease_until=NULL WHERE job_id=$1 AND claim_token=$2 AND file_key=$4 AND reserved_bytes >= $3 AND $3 >= 0`, j.ID, j.ClaimToken, j.SizeBytes, j.FileKey)
		if err != nil {
			return false, err
		}
		updated, _ := res.RowsAffected()
		if updated != 1 {
			return false, errors.New("usage export artifact reservation missing")
		}
	}
	if n == 0 || j.Status != "completed" {
		if _, err = tx.ExecContext(ctx, `UPDATE usage_export_artifacts SET lease_until=NULL WHERE job_id=$1 AND claim_token=$2`, j.ID, j.ClaimToken); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}
func (r *usageExportRepository) Cancel(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE usage_export_jobs SET status='cancelled',expires_at=NOW()+INTERVAL '24 hours',ticket_hash=NULL WHERE id=$1 AND status IN ('queued','running')`, id)
	return err
}
func (r *usageExportRepository) SetTicket(ctx context.Context, id, hash string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE usage_export_jobs SET ticket_hash=$2,ticket_until=NOW()+INTERVAL '60 seconds' WHERE id=$1 AND status='completed' AND expires_at>NOW()`, id, hash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (r *usageExportRepository) Expired(ctx context.Context) ([]service.UsageExportJob, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+exportColumns+` FROM usage_export_jobs WHERE expires_at<NOW() ORDER BY expires_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]service.UsageExportJob, 0)
	for rows.Next() {
		j, e := scanExport(rows)
		if e != nil {
			return nil, e
		}
		jobs = append(jobs, *j)
	}
	return jobs, rows.Err()
}
