-- Confirmed Engine usage can arrive before the downstream response is complete.
-- Keep old jobs billable for compatibility; new jobs explicitly start pending.
ALTER TABLE codex_engine_billing_jobs
    ADD COLUMN IF NOT EXISTS delivery_outcome VARCHAR(24) NOT NULL DEFAULT 'delivered'
    CHECK (delivery_outcome IN ('pending', 'delivered', 'no_customer_charge'));

-- Pending delivery and incomplete requests are deliberately not polled. Keep
-- their retained job rows out of the due scan as the history grows.
CREATE INDEX IF NOT EXISTS codex_engine_billing_jobs_delivered_due_idx
    ON codex_engine_billing_jobs (next_attempt_at, id)
    WHERE delivery_outcome = 'delivered' AND state IN ('pending', 'blocked');
