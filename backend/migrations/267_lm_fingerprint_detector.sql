-- Changing the analyzer requires an explicit administrator connection check.
SELECT pg_advisory_xact_lock(263, 1);
UPDATE model_attribution_config
SET version=version+1,
    config=(config - 'detector') || '{"enabled":false,"base_url":""}'::jsonb,
    updated_at=NOW();
UPDATE model_attribution_state SET pass_streak=0;
UPDATE model_attribution_jobs
SET status='failed',reason='detector_changed',finished_at=NOW(),lease_until=NULL,
    result=result || '{"action":"none"}'::jsonb
WHERE status IN ('queued','running');
