-- Harden group-application state transitions and mail delivery ownership.

ALTER TABLE group_applications
    ADD COLUMN IF NOT EXISTS access_grant_owned BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE group_application_mail_outbox
    ADD COLUMN IF NOT EXISTS claim_expires_at TIMESTAMPTZ;

-- The original completion transaction used PostgreSQL NOW() for both rows.
-- NOW() is transaction-stable, so equality proves that the application created
-- this legacy authorization rather than merely observing a manual grant.
UPDATE group_applications application
SET access_grant_owned = TRUE
FROM user_allowed_groups allowed
WHERE application.status = 'completed'
  AND application.completed_at IS NOT NULL
  AND allowed.user_id = application.user_id
  AND allowed.group_id = application.group_id
  AND allowed.created_at = application.completed_at;

-- Preserve the effective lease of jobs claimed before this column existed.
UPDATE group_application_mail_outbox
SET claim_expires_at = claimed_at + INTERVAL '1 minute'
WHERE status = 'processing'
  AND claimed_at IS NOT NULL
  AND claim_expires_at IS NULL;

-- An already-authorized user cannot complete another active application. Close
-- any such legacy workflow before enforcing the unified state invariant.
UPDATE group_applications a
SET status = 'rejected',
    reviewed_at = COALESCE(a.reviewed_at, NOW()),
    decision_reason = COALESCE(a.decision_reason, 'access_already_granted'),
    updated_at = NOW()
WHERE a.status IN ('pending', 'awaiting_reply')
  AND (
      EXISTS (
          SELECT 1
          FROM group_applications completed
          WHERE completed.user_id = a.user_id
            AND completed.group_id = a.group_id
            AND completed.status = 'completed'
      )
      OR EXISTS (
          SELECT 1
          FROM user_allowed_groups allowed
          WHERE allowed.user_id = a.user_id
            AND allowed.group_id = a.group_id
      )
  );

UPDATE group_application_mail_outbox o
SET status = 'cancelled',
    claimed_at = NULL,
    claimed_by = NULL,
    claim_expires_at = NULL,
    last_error = 'application status changed',
    updated_at = NOW()
FROM group_applications a
WHERE a.id = o.application_id
  AND o.status IN ('pending', 'processing')
  AND a.status <> o.required_application_status;

DROP INDEX IF EXISTS group_applications_one_active_per_user_group;
DROP INDEX IF EXISTS group_applications_one_completed_per_user_group;
CREATE UNIQUE INDEX IF NOT EXISTS group_applications_one_open_or_completed_per_user_group
    ON group_applications(user_id, group_id)
    WHERE status IN ('pending', 'awaiting_reply', 'completed');

-- Retain the currently-processing approval where possible, otherwise the
-- oldest pending one. All other active duplicates are terminally cancelled.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY application_id, required_application_status
               ORDER BY CASE WHEN status = 'processing' THEN 0 ELSE 1 END, id
           ) AS position
    FROM group_application_mail_outbox
    WHERE kind = 'approval'
      AND status IN ('pending', 'processing')
)
UPDATE group_application_mail_outbox o
SET status = 'cancelled',
    claimed_at = NULL,
    claimed_by = NULL,
    claim_expires_at = NULL,
    last_error = 'duplicate approval mail',
    updated_at = NOW()
FROM ranked
WHERE ranked.id = o.id
  AND ranked.position > 1;

CREATE UNIQUE INDEX IF NOT EXISTS group_application_mail_outbox_one_active_approval
    ON group_application_mail_outbox(application_id, required_application_status)
    WHERE kind = 'approval' AND status IN ('pending', 'processing');

COMMENT ON COLUMN group_applications.access_grant_owned IS
    'True only when this application inserted the user_allowed_groups row; revocation may delete only an owned grant.';
COMMENT ON COLUMN group_application_mail_outbox.claim_expires_at IS
    'Delivery lease refreshed immediately before SMTP send; terminal application transitions wait for an unexpired lease.';
