ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS grok_safety_fee_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS grok_safety_fee DECIMAL(20,8);

COMMENT ON COLUMN groups.grok_safety_fee_enabled IS 'Charge a fixed fee for an exact upstream Grok safety rejection; Grok groups only';
COMMENT ON COLUMN groups.grok_safety_fee IS 'Fixed USD fee independent of multipliers; null or zero does not charge';
