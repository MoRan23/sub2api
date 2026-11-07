-- Total quota belongs to each grant. Existing groups remain time-limited.
ALTER TABLE groups ADD COLUMN IF NOT EXISTS total_quota_no_expiry BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS quota_amount DECIMAL(20,8);
ALTER TABLE user_subscriptions ALTER COLUMN expires_at DROP NOT NULL;
ALTER TABLE user_subscription_quota_events ALTER COLUMN expires_at DROP NOT NULL;

-- Old unused codes have no amount snapshot and must not silently inherit a
-- group template. Used codes and their already-issued quota events are intact.
UPDATE redeem_codes r
SET status = 'disabled'
FROM groups g
WHERE r.group_id = g.id
  AND r.type = 'subscription'
  AND g.subscription_type = 'total_quota'
  AND r.status <> 'used'
  AND r.used_by IS NULL AND r.used_at IS NULL
  AND r.quota_amount IS NULL;

-- Stop total-quota automatic grants while preserving other entries and order.
DO $$
DECLARE
    setting_row RECORD;
    entries JSONB;
    filtered JSONB;
BEGIN
    FOR setting_row IN
        SELECT id, value FROM settings
        WHERE key = 'default_subscriptions'
           OR key ~ '^auth_source_default_[a-z]+_subscriptions$'
    LOOP
        BEGIN
            entries := setting_row.value::jsonb;
        EXCEPTION WHEN invalid_text_representation THEN
            CONTINUE;
        END;
        IF jsonb_typeof(entries) IS DISTINCT FROM 'array' THEN
            CONTINUE;
        END IF;
        SELECT COALESCE(jsonb_agg(item ORDER BY ord), '[]'::jsonb)
        INTO filtered
        FROM jsonb_array_elements(entries) WITH ORDINALITY AS e(item, ord)
        WHERE NOT EXISTS (
            SELECT 1 FROM groups g
            WHERE g.id::text = item->>'group_id'
              AND g.subscription_type = 'total_quota'
        );
        IF filtered IS DISTINCT FROM entries THEN
            UPDATE settings SET value = filtered::text, updated_at = NOW()
            WHERE id = setting_row.id;
        END IF;
    END LOOP;
END $$;
