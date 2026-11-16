package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const subjectRollupColumns = "request_count,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum_ms,duration_count,charged_tokens,charged_cost,success_count"
const subjectRollupAggregate = `
 COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),
 COALESCE(SUM(cache_creation_tokens),0),COALESCE(SUM(cache_read_tokens),0),
 COALESCE(SUM(total_cost),0),COALESCE(SUM(actual_cost),0),
 COALESCE(SUM(duration_ms),0),COUNT(duration_ms),
 COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) FILTER(WHERE actual_cost>0),0),
 COALESCE(SUM(actual_cost) FILTER(WHERE actual_cost>0),0),
 COUNT(*) FILTER(WHERE actual_cost>0 AND grok_safety_rejection IS NULL)`

// A connection advisory lock bounds duplicate builders without blocking inserts.
// The state row is locked only during prepare/publication, never while scanning.
func syncUsageDailyRollups(ctx context.Context, db *sql.DB, kind string, today time.Time) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	lockID := int64(1)
	if kind == "subject" {
		lockID = 2
	}
	var acquired bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(269,$1)", lockID).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.ExecContext(c, "SELECT pg_advisory_unlock(269,$1)", lockID); unlockErr != nil {
			// Never return a session with an outstanding advisory lock to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	state := "usage_" + kind + "_rollup_state"
	stage := "usage_" + kind + "_rollup_stage"
	table := "usage_" + kind + "_daily_rollups"
	tz := service.GroupUsageTimezoneName()
	today = service.GroupUsageTodayStart(today)
	// Orphan stages from a crashed builder cannot be published and have no readers.
	if _, err = conn.ExecContext(ctx, "DELETE FROM "+stage); err != nil {
		return err
	}
	for conflicts := 0; ; {
		if err = ctx.Err(); err != nil {
			return err
		}
		tx, e := conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		var closed, oldTZ string
		var previousRetained time.Time
		e = tx.QueryRowContext(ctx, "SELECT closed_before::text,timezone_name,retained_from FROM "+state+" WHERE id=1 FOR UPDATE").Scan(&closed, &oldTZ, &previousRetained)
		if e != nil {
			tx.Rollback()
			return e
		}
		var earliest sql.NullTime
		e = tx.QueryRowContext(ctx, "SELECT MIN(created_at) FROM usage_logs").Scan(&earliest)
		if e != nil {
			tx.Rollback()
			return e
		}
		retained := today
		if earliest.Valid {
			retained = earliest.Time
		}
		first := service.GroupUsageDate(retained)
		if first > service.GroupUsageDate(today) {
			first = service.GroupUsageDate(today)
		}
		if oldTZ != tz {
			closed = first
		}
		if closed < first {
			closed = first
		}
		if closed > service.GroupUsageDate(today) {
			tx.Rollback()
			return fmt.Errorf("usage %s rollup watermark is in the future: %s", kind, closed)
		}
		start, e := service.ParseGroupUsageDate(closed)
		if e != nil {
			tx.Rollback()
			return e
		}
		if !start.Before(today) {
			_, e = tx.ExecContext(ctx, "UPDATE "+state+" SET closed_before=$1::date,retained_from=$2,timezone_name=$3,build_token=NULL,building_before=NULL,updated_at=NOW() WHERE id=1", closed, retained, tz)
			if e != nil {
				tx.Rollback()
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			_, e = conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE bucket_date < $1::date OR bucket_date >= $2::date", first, service.GroupUsageDate(today))
			return e
		}
		end := start.AddDate(0, 0, 1)
		token := uuid.NewString()
		var revision int64
		e = tx.QueryRowContext(ctx, "UPDATE "+state+" SET closed_before=$1::date,retained_from=$2,timezone_name=$3,build_token=$4::uuid,building_before=$5::date,updated_at=NOW() WHERE id=1 RETURNING revision", closed, retained, tz, token, service.GroupUsageDate(end)).Scan(&revision)
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		if kind == "group" {
			_, err = conn.ExecContext(ctx, "INSERT INTO "+stage+"(build_token,bucket_date,group_id,actual_cost) SELECT $1::uuid,$2::date,group_id,COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE group_id IS NOT NULL AND created_at >= $3 AND created_at < $4 GROUP BY group_id", token, closed, start.UTC(), end.UTC())
		} else {
			_, err = conn.ExecContext(ctx, "INSERT INTO "+stage+"(build_token,bucket_date,user_id,api_key_id,group_id,account_id,"+subjectRollupColumns+") SELECT $1::uuid,$2::date,COALESCE(user_id,0),COALESCE(api_key_id,0),COALESCE(group_id,0),COALESCE(account_id,0),"+subjectRollupAggregate+" FROM usage_logs WHERE created_at >= $3 AND created_at < $4 GROUP BY user_id,api_key_id,group_id,account_id", token, closed, start.UTC(), end.UTC())
		}
		if err != nil {
			return err
		}
		tx, e = conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		var currentRevision int64
		var currentToken sql.NullString
		var currentClosed, currentTZ string
		e = tx.QueryRowContext(ctx, "SELECT revision,build_token::text,closed_before::text,timezone_name FROM "+state+" WHERE id=1 FOR UPDATE").Scan(&currentRevision, &currentToken, &currentClosed, &currentTZ)
		if e != nil {
			tx.Rollback()
			return e
		}
		if currentRevision != revision || currentToken.String != token || currentClosed != closed || currentTZ != tz {
			tx.Rollback()
			if _, e = conn.ExecContext(ctx, "DELETE FROM "+stage+" WHERE build_token=$1::uuid", token); e != nil {
				return e
			}
			conflicts++
			if conflicts >= 3 {
				return fmt.Errorf("usage %s rollup changed during build; retry on next tick", kind)
			}
			continue
		}
		_, e = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE bucket_date=$1::date", closed)
		if e == nil {
			cols := "bucket_date,group_id,actual_cost,computed_at"
			if kind == "subject" {
				cols = "bucket_date,user_id,api_key_id,group_id,account_id," + subjectRollupColumns + ",computed_at"
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO "+table+"("+cols+") SELECT "+cols+" FROM "+stage+" WHERE build_token=$1::uuid", token)
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, "UPDATE "+state+" SET closed_before=$1::date,build_token=NULL,building_before=NULL,updated_at=NOW() WHERE id=1", service.GroupUsageDate(end))
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, "DELETE FROM "+stage+" WHERE build_token=$1::uuid", token)
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		conflicts = 0
	}
}
