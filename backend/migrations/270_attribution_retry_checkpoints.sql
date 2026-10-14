-- Private recovery data belongs only to active attribution jobs. It is never
-- included in the public result and is cleared when a job ends or is cancelled.
ALTER TABLE model_attribution_jobs ADD COLUMN IF NOT EXISTS checkpoint JSONB;
ALTER TABLE model_attribution_jobs ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;
ALTER TABLE model_attribution_jobs DROP CONSTRAINT IF EXISTS model_attribution_jobs_status_check;
ALTER TABLE model_attribution_jobs ADD CONSTRAINT model_attribution_jobs_status_check
    CHECK(status IN ('queued','running','retry_wait','passed','mismatch','abnormal','failed','skipped','cancelled'));

DROP INDEX IF EXISTS model_attribution_active_account;
CREATE UNIQUE INDEX model_attribution_active_account ON model_attribution_jobs(account_id)
    WHERE status IN ('queued','running','retry_wait');
CREATE INDEX IF NOT EXISTS model_attribution_retry_due ON model_attribution_jobs(next_attempt_at,id)
    WHERE status='retry_wait';

-- Old workers did not persist a restart-safe probe position. Never replay an
-- ambiguous old run, and do not infer override choices from historical output.
WITH ended AS (
    UPDATE model_attribution_jobs
       SET status='failed',reason='interrupted',finished_at=NOW(),
           claim_id=NULL,lease_until=NULL,checkpoint=NULL,next_attempt_at=NULL
     WHERE status IN ('queued','running')
       AND COALESCE((snapshot->>'version')::integer,0)<2
     RETURNING account_id
)
UPDATE model_attribution_state SET pass_streak=0
 WHERE account_id IN (SELECT account_id FROM ended);
