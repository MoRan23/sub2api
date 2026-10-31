-- Administrator wallet history records signed changes. Redeemable codes
-- continue to require nonnegative gift values.
ALTER TABLE redeem_codes
    DROP CONSTRAINT IF EXISTS redeem_codes_gift_value_nonnegative,
    ADD CONSTRAINT redeem_codes_gift_value_nonnegative
        CHECK (gift_value >= 0 OR type = 'admin_balance') NOT VALID;
