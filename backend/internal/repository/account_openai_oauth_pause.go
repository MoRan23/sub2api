package repository

import (
	"context"
	"database/sql"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const openAIOAuthPauseMessage = "OAuth authorization must be renewed"
const openAIOAuthRetryMessage = "OAuth token refresh temporarily unavailable"

// All callers hold the account row lock. Capture ownership before changing the
// row: the ordinary state-write trigger deliberately clears previous ownership.
func pauseOpenAIOAuthAccountLocked(ctx context.Context, client *dbent.Client, id int64) error {
	rows, err := client.QueryContext(ctx, `SELECT CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_status ELSE a.status END,
	CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_schedulable ELSE a.schedulable END,
	CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_error_message ELSE COALESCE(a.error_message,'') END
	FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`, id)
	if err != nil {
		return err
	}
	var status, message string
	var schedulable bool
	if !rows.Next() {
		rows.Close()
		return service.ErrAccountNotFound
	}
	err = rows.Scan(&status, &schedulable, &message)
	rows.Close()
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `UPDATE accounts SET status='error',schedulable=false,error_message=$2,updated_at=NOW() WHERE id=$1`, id, openAIOAuthPauseMessage)
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET auth_pause_owned=true,auth_pause_previous_status=$2,auth_pause_previous_schedulable=$3,auth_pause_previous_error_message=$4 WHERE account_id=$1`, id, status, schedulable, message)
	return err
}

func cooldownOpenAIOAuthAccountLocked(ctx context.Context, client *dbent.Client, id int64, until time.Time) error {
	rows, err := client.QueryContext(ctx, `SELECT a.temp_unschedulable_until,COALESCE(a.temp_unschedulable_reason,''),c.auth_retry_owned,c.auth_retry_previous_until,COALESCE(c.auth_retry_previous_reason,'') FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`, id)
	if err != nil {
		return err
	}
	var current, previous sql.NullTime
	var reason, previousReason string
	var owned bool
	if !rows.Next() {
		rows.Close()
		return service.ErrAccountNotFound
	}
	err = rows.Scan(&current, &reason, &owned, &previous, &previousReason)
	rows.Close()
	if err != nil {
		return err
	}
	if current.Valid && current.Time.After(until) {
		return nil
	}
	if !owned {
		previous = current
		previousReason = reason
	}
	_, err = client.ExecContext(ctx, `UPDATE accounts SET temp_unschedulable_until=$2,temp_unschedulable_reason=$3,updated_at=NOW() WHERE id=$1`, id, until, openAIOAuthRetryMessage)
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET auth_retry_owned=true,auth_retry_until=$2,auth_retry_previous_until=$3,auth_retry_previous_reason=$4 WHERE account_id=$1`, id, until, previous, previousReason)
	return err
}

func restoreOpenAIOAuthOwnedPauseLocked(ctx context.Context, client *dbent.Client, id int64, reauthorized bool) error {
	if reauthorized {
		_, err := client.ExecContext(ctx, `UPDATE accounts a SET status=c.auth_pause_previous_status,schedulable=c.auth_pause_previous_schedulable,error_message=c.auth_pause_previous_error_message,updated_at=NOW()
		FROM account_openai_oauth_credentials c WHERE a.id=$1 AND c.account_id=a.id AND c.auth_pause_owned AND a.status='error' AND NOT a.schedulable`, id)
		if err != nil {
			return err
		}
		_, err = client.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET auth_pause_owned=false WHERE account_id=$1`, id)
		if err != nil {
			return err
		}
	}
	_, err := client.ExecContext(ctx, `UPDATE accounts a SET temp_unschedulable_until=c.auth_retry_previous_until,temp_unschedulable_reason=c.auth_retry_previous_reason,updated_at=NOW()
	FROM account_openai_oauth_credentials c WHERE a.id=$1 AND c.account_id=a.id AND c.auth_retry_owned AND a.temp_unschedulable_until=c.auth_retry_until AND a.temp_unschedulable_reason=$2`, id, openAIOAuthRetryMessage)
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET auth_retry_owned=false,auth_retry_until=NULL WHERE account_id=$1`, id)
	return err
}

// A general account snapshot must not undo a later authentication pause. Only
// explicitly supplied admin state or dedicated state writers surrender ownership.
func preserveOpenAIOAuthOwnedPauseLocked(ctx context.Context, client *dbent.Client, account *service.Account) (bool, error) {
	if service.OpenAIOAuthAccountStateIntentAllowed(ctx, account.ID) {
		return false, nil
	}
	rows, err := client.QueryContext(ctx, `SELECT a.status,a.schedulable,COALESCE(a.error_message,'') FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1 AND c.auth_pause_owned`, account.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	err = rows.Scan(&account.Status, &account.Schedulable, &account.ErrorMessage)
	return true, err
}
