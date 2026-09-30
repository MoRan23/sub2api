-- Stop old application workers before applying this migration. Retain completed
-- candy history, but never replay outstanding candy jobs with a different prompt.
ALTER TABLE account_candy_test_items DROP CONSTRAINT account_candy_test_items_status_check;
ALTER TABLE account_candy_test_items ADD CONSTRAINT account_candy_test_items_status_check
    CHECK (status IN ('queued','running','normal','generated','abnormal','failed','cancelled','skipped'));

UPDATE account_candy_test_items
SET status='cancelled', cancel_requested=TRUE, failure_code='test_replaced',
    finished_at=NOW(), claim_id=NULL, lease_until=NULL
WHERE prompt_version <> 'pelican-v1' AND status IN ('queued','running');

UPDATE account_candy_test_batches b
SET counts=q.counts, finished_at=NOW()
FROM (
    SELECT batch_id, jsonb_object_agg(status,n) AS counts
    FROM (
        SELECT batch_id,status,count(*) AS n FROM account_candy_test_items
        WHERE prompt_version <> 'pelican-v1' GROUP BY batch_id,status
    ) grouped GROUP BY batch_id
) q
WHERE b.id=q.batch_id AND b.prompt_version <> 'pelican-v1' AND b.finished_at IS NULL;
