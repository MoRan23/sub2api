// Package promptenforcement contains small SQL helpers shared by the user
// repository and prompt-audit enforcement. It deliberately has no service or
// Ent dependency so the repository package can use it without an import cycle.
package promptenforcement

import (
	"context"
	"database/sql"
	"time"
)

type ExecQuerier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// ApplyExplicitUserStatus invalidates prompt-audit enforcement ownership when
// an administrator explicitly writes a user status. The caller must first
// lock the user row and call this helper in the same transaction. It updates
// no row when the user has never had an enforcement state.
func ApplyExplicitUserStatus(ctx context.Context, q ExecQuerier, userID int64, status string, now time.Time) error {
	if q == nil || userID <= 0 {
		return nil
	}
	_, err := q.ExecContext(ctx, `
		UPDATE prompt_audit_enforcement_states
		SET revision = revision + 1,
		    owned_ban_revision = NULL,
		    ban_event_id = NULL,
		    counting_since = CASE WHEN $2 = 'active' THEN $3 ELSE counting_since END,
		    epoch = CASE WHEN $2 = 'active' THEN epoch + 1 ELSE epoch END,
		    updated_at = $3
		WHERE user_id = $1`, userID, status, now.UTC())
	if err != nil {
		return err
	}
	if status == "active" {
		_, err = q.ExecContext(ctx, `UPDATE prompt_audit_enforcement_notifications SET sent_at=COALESCE(sent_at,$2),claimed_at=NULL,last_error='cycle_reset',updated_at=$2 WHERE user_id=$1 AND sent_at IS NULL`, userID, now.UTC())
	}
	return err
}
