package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func lockUsageExportJob(ctx context.Context, tx *sql.Tx, id string) (*service.UsageExportJob, bool, error) {
	var live bool
	j, err := scanExport(tx.QueryRowContext(ctx, `SELECT `+exportColumns+`,COALESCE(lease_until>NOW(),false) FROM usage_export_jobs WHERE id=$1 FOR UPDATE`, id), &live)
	return j, live, err
}

func (r *usageExportRepository) PeekByTicket(ctx context.Context, hash string) (*service.UsageExportJob, error) {
	return scanExport(r.db.QueryRowContext(ctx, `SELECT `+exportColumns+` FROM usage_export_jobs WHERE ticket_hash=$1 AND ticket_until>NOW() AND expires_at>NOW() AND status='completed'`, hash))
}

func (r *usageExportRepository) GetByTicket(ctx context.Context, hash string, open func(*service.UsageExportJob) error) (*service.UsageExportJob, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	j, err := scanExport(tx.QueryRowContext(ctx, `SELECT `+exportColumns+` FROM usage_export_jobs WHERE ticket_hash=$1 AND ticket_until>NOW() AND expires_at>NOW() AND status='completed' FOR UPDATE`, hash))
	if err != nil {
		return nil, err
	}
	var key string
	var downloading bool
	if err = tx.QueryRowContext(ctx, `SELECT file_key,COALESCE(download_until>NOW(),false) FROM usage_export_artifacts WHERE job_id=$1 AND claim_token=$2 AND file_key=$3 FOR UPDATE`, j.ID, j.ClaimToken, j.FileKey).Scan(&key, &downloading); err != nil {
		return nil, err
	}
	if downloading {
		return nil, sql.ErrNoRows
	}
	// Authorization runs before this transaction. Here the caller verifies the
	// frozen owner and opens the file under the same job -> artifact locks as
	// deletion. Neither database authentication nor body reads hold these locks.
	if err = open(j); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_export_artifacts SET download_token=$3,download_until=NOW()+INTERVAL '60 seconds' WHERE job_id=$1 AND claim_token=$2`, j.ID, j.ClaimToken, hash); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_export_jobs SET ticket_hash=NULL WHERE id=$1`, j.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return j, nil
}

func (r *usageExportRepository) RenewDownload(ctx context.Context, id, claim, token string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE usage_export_artifacts a SET download_until=NOW()+INTERVAL '60 seconds' WHERE job_id=$1 AND claim_token=$2 AND download_token=$3 AND download_until>NOW() AND EXISTS(SELECT 1 FROM usage_export_jobs j WHERE j.id=a.job_id)`, id, claim, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func (r *usageExportRepository) EndDownload(ctx context.Context, id, claim, token string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE usage_export_artifacts SET download_token='',download_until=NULL WHERE job_id=$1 AND claim_token=$2 AND download_token=$3`, id, claim, token)
	return err
}

func (r *usageExportRepository) Delete(ctx context.Context, id string, remove func(service.UsageExportArtifact) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	j, live, err := lockUsageExportJob(ctx, tx, id)
	if err != nil {
		return err
	}
	if live || j.Status == "queued" || j.Status == "running" {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_export_jobs SET status='deleting',ticket_hash=NULL,expires_at=NOW() WHERE id=$1`, id); err != nil {
		return err
	}
	// Make the artifact undiscoverable before any destructive filesystem work.
	// If the first commit is ambiguous we do not unlink; the next cleanup retries.
	if err = tx.Commit(); err != nil {
		return err
	}
	tx, err = r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	j, _, err = lockUsageExportJob(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Status != "deleting" {
		return sql.ErrNoRows
	}
	rows, err := tx.QueryContext(ctx, `SELECT job_id,claim_token,file_key,reserved_bytes,COALESCE(lease_until>NOW(),false) OR COALESCE(download_until>NOW(),false) FROM usage_export_artifacts WHERE job_id=$1 ORDER BY claim_token FOR UPDATE`, id)
	if err != nil {
		return err
	}
	var artifacts []service.UsageExportArtifact
	for rows.Next() {
		var a service.UsageExportArtifact
		var live bool
		if err = rows.Scan(&a.JobID, &a.ClaimToken, &a.FileKey, &a.ReservedBytes, &live); err != nil {
			rows.Close()
			return err
		}
		if live {
			rows.Close()
			// Deletion was accepted in the first transaction. Keep the file and
			// reservation until the active writer/download releases its lease.
			return nil
		}
		artifacts = append(artifacts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var removeErr error
	for _, a := range artifacts {
		if removeErr = remove(a); removeErr != nil {
			break
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM usage_export_artifacts WHERE job_id=$1 AND claim_token=$2`, a.JobID, a.ClaimToken); err != nil {
			return err
		}
	}
	if removeErr == nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM usage_export_jobs WHERE id=$1`, id); err != nil {
			return err
		}
	}
	// Preserve deleting status and remaining reservations if the filesystem is
	// temporarily unavailable. Cleanup can retry without losing quota ownership.
	if err = tx.Commit(); err != nil {
		return err
	}
	return removeErr
}

func (r *usageExportRepository) CleanupArtifacts(ctx context.Context) ([]service.UsageExportArtifact, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT a.job_id,a.claim_token,a.file_key,a.reserved_bytes FROM usage_export_artifacts a LEFT JOIN usage_export_jobs j ON j.id=a.job_id WHERE NOT COALESCE(a.lease_until>NOW(),false) AND NOT COALESCE(a.download_until>NOW(),false) AND (j.id IS NULL OR ((j.status<>'completed' OR j.file_key<>a.file_key) AND NOT COALESCE(j.claim_token=a.claim_token AND j.lease_until>NOW(),false))) ORDER BY a.created_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.UsageExportArtifact, 0)
	for rows.Next() {
		var a service.UsageExportArtifact
		if err = rows.Scan(&a.JobID, &a.ClaimToken, &a.FileKey, &a.ReservedBytes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *usageExportRepository) ReleaseArtifact(ctx context.Context, a service.UsageExportArtifact, remove func(service.UsageExportArtifact) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	j, live, err := lockUsageExportJob(ctx, tx, a.JobID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if j != nil && j.ClaimToken == a.ClaimToken && (j.Status == "completed" || live) {
		return nil
	}
	var actual service.UsageExportArtifact
	var artifactLive bool
	err = tx.QueryRowContext(ctx, `SELECT job_id,claim_token,file_key,reserved_bytes,COALESCE(lease_until>NOW(),false) OR COALESCE(download_until>NOW(),false) FROM usage_export_artifacts WHERE job_id=$1 AND claim_token=$2 FOR UPDATE`, a.JobID, a.ClaimToken).Scan(&actual.JobID, &actual.ClaimToken, &actual.FileKey, &actual.ReservedBytes, &artifactLive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if artifactLive {
		return nil
	}
	if err = remove(actual); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM usage_export_artifacts WHERE job_id=$1 AND claim_token=$2`, a.JobID, a.ClaimToken); err != nil {
		return err
	}
	return tx.Commit()
}
