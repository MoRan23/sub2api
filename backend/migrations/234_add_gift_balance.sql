-- Add a separate promotional balance while preserving existing balance semantics.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS gift_balance NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS frozen_gift_balance NUMERIC(20,8) NOT NULL DEFAULT 0;

ALTER TABLE redeem_codes
    ADD COLUMN IF NOT EXISTS gift_ratio NUMERIC(10,4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS gift_value NUMERIC(20,8) NOT NULL DEFAULT 0;

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS gift_ratio NUMERIC(10,4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS gift_amount NUMERIC(20,8) NOT NULL DEFAULT 0;

ALTER TABLE usage_billing_dedup
    ADD COLUMN IF NOT EXISTS ordinary_hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS gift_hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS hold_terminal_kind VARCHAR(16) NOT NULL DEFAULT '';

ALTER TABLE usage_billing_dedup_archive
    ADD COLUMN IF NOT EXISTS ordinary_hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS gift_hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS hold_terminal_kind VARCHAR(16) NOT NULL DEFAULT '';

-- Before gift-wallet provenance existed, batch-image hold/capture/release used
-- separate idempotency receipts. Recover the terminal state across both the hot
-- and archive tables so an already-settled historical hold cannot be settled
-- again against another job's pooled frozen balance. A historical capture wins
-- over release if corrupted legacy data contains both terminal receipts.
WITH historical_terminals AS (
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_capture:') + 1) AS batch_id,
           'captured'::VARCHAR(16) AS terminal_kind
    FROM usage_billing_dedup
    WHERE request_id LIKE 'batch_image_capture:%'
    UNION ALL
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_capture:') + 1),
           'captured'::VARCHAR(16)
    FROM usage_billing_dedup_archive
    WHERE request_id LIKE 'batch_image_capture:%'
    UNION ALL
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_release:') + 1),
           'released'::VARCHAR(16)
    FROM usage_billing_dedup
    WHERE request_id LIKE 'batch_image_release:%'
    UNION ALL
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_release:') + 1),
           'released'::VARCHAR(16)
    FROM usage_billing_dedup_archive
    WHERE request_id LIKE 'batch_image_release:%'
), resolved_terminals AS (
    SELECT api_key_id,
           batch_id,
           CASE WHEN BOOL_OR(terminal_kind = 'captured') THEN 'captured' ELSE 'released' END AS terminal_kind
    FROM historical_terminals
    GROUP BY api_key_id, batch_id
)
UPDATE usage_billing_dedup AS hold
SET hold_terminal_kind = terminal.terminal_kind
FROM resolved_terminals AS terminal
WHERE hold.api_key_id = terminal.api_key_id
  AND hold.request_id = 'batch_image_hold:' || terminal.batch_id
  AND hold.hold_terminal_kind = '';

WITH historical_terminals AS (
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_capture:') + 1) AS batch_id,
           'captured'::VARCHAR(16) AS terminal_kind
    FROM usage_billing_dedup
    WHERE request_id LIKE 'batch_image_capture:%'
    UNION ALL
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_capture:') + 1),
           'captured'::VARCHAR(16)
    FROM usage_billing_dedup_archive
    WHERE request_id LIKE 'batch_image_capture:%'
    UNION ALL
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_release:') + 1),
           'released'::VARCHAR(16)
    FROM usage_billing_dedup
    WHERE request_id LIKE 'batch_image_release:%'
    UNION ALL
    SELECT api_key_id,
           SUBSTRING(request_id FROM CHAR_LENGTH('batch_image_release:') + 1),
           'released'::VARCHAR(16)
    FROM usage_billing_dedup_archive
    WHERE request_id LIKE 'batch_image_release:%'
), resolved_terminals AS (
    SELECT api_key_id,
           batch_id,
           CASE WHEN BOOL_OR(terminal_kind = 'captured') THEN 'captured' ELSE 'released' END AS terminal_kind
    FROM historical_terminals
    GROUP BY api_key_id, batch_id
)
UPDATE usage_billing_dedup_archive AS hold
SET hold_terminal_kind = terminal.terminal_kind
FROM resolved_terminals AS terminal
WHERE hold.api_key_id = terminal.api_key_id
  AND hold.request_id = 'batch_image_hold:' || terminal.batch_id
  AND hold.hold_terminal_kind = '';

ALTER TABLE usage_billing_dedup
    DROP CONSTRAINT IF EXISTS usage_billing_dedup_ordinary_hold_nonnegative,
    DROP CONSTRAINT IF EXISTS usage_billing_dedup_gift_hold_nonnegative,
    DROP CONSTRAINT IF EXISTS usage_billing_dedup_hold_terminal_kind_valid,
    ADD CONSTRAINT usage_billing_dedup_ordinary_hold_nonnegative
        CHECK (ordinary_hold_amount >= 0) NOT VALID,
    ADD CONSTRAINT usage_billing_dedup_gift_hold_nonnegative
        CHECK (gift_hold_amount >= 0) NOT VALID,
    ADD CONSTRAINT usage_billing_dedup_hold_terminal_kind_valid
        CHECK (hold_terminal_kind IN ('', 'captured', 'released')) NOT VALID;

ALTER TABLE usage_billing_dedup_archive
    DROP CONSTRAINT IF EXISTS usage_billing_dedup_archive_ordinary_hold_nonnegative,
    DROP CONSTRAINT IF EXISTS usage_billing_dedup_archive_gift_hold_nonnegative,
    DROP CONSTRAINT IF EXISTS usage_billing_dedup_archive_hold_terminal_kind_valid,
    ADD CONSTRAINT usage_billing_dedup_archive_ordinary_hold_nonnegative
        CHECK (ordinary_hold_amount >= 0) NOT VALID,
    ADD CONSTRAINT usage_billing_dedup_archive_gift_hold_nonnegative
        CHECK (gift_hold_amount >= 0) NOT VALID,
    ADD CONSTRAINT usage_billing_dedup_archive_hold_terminal_kind_valid
        CHECK (hold_terminal_kind IN ('', 'captured', 'released')) NOT VALID;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_gift_balance_nonnegative,
    DROP CONSTRAINT IF EXISTS users_frozen_gift_balance_nonnegative,
    ADD CONSTRAINT users_gift_balance_nonnegative CHECK (gift_balance >= 0) NOT VALID,
    ADD CONSTRAINT users_frozen_gift_balance_nonnegative CHECK (frozen_gift_balance >= 0) NOT VALID;

ALTER TABLE redeem_codes
    DROP CONSTRAINT IF EXISTS redeem_codes_gift_ratio_range,
    DROP CONSTRAINT IF EXISTS redeem_codes_gift_value_nonnegative,
    ADD CONSTRAINT redeem_codes_gift_ratio_range CHECK (gift_ratio >= 0 AND gift_ratio <= 100) NOT VALID,
    ADD CONSTRAINT redeem_codes_gift_value_nonnegative CHECK (gift_value >= 0) NOT VALID;

ALTER TABLE payment_orders
    DROP CONSTRAINT IF EXISTS payment_orders_gift_ratio_range,
    DROP CONSTRAINT IF EXISTS payment_orders_gift_amount_nonnegative,
    ADD CONSTRAINT payment_orders_gift_ratio_range CHECK (gift_ratio >= 0 AND gift_ratio <= 100) NOT VALID,
    ADD CONSTRAINT payment_orders_gift_amount_nonnegative CHECK (gift_amount >= 0) NOT VALID;
