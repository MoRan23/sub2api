-- NULL inherits the account policy; [] follows the initial probe. Existing
-- events keep inheritance, and all existing settings/history remain unchanged.
ALTER TABLE account_initial_tests ADD COLUMN attribution_expected_models JSONB
    CHECK (attribution_expected_models IS NULL OR jsonb_typeof(attribution_expected_models) = 'array');
