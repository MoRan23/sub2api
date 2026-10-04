-- Keep the existing model column as the attribution snapshot. Already-created
-- events retain their original pelican model, including unfinished events.
ALTER TABLE account_initial_tests ADD COLUMN pelican_model TEXT;
UPDATE account_initial_tests SET pelican_model=model;
ALTER TABLE account_initial_tests ALTER COLUMN pelican_model SET NOT NULL;

-- Preserve custom shared models. Only the old default pelican choice changes.
-- Bump the version so an open editor or running attribution cannot apply an
-- obsolete configuration after the upgrade.
UPDATE model_attribution_config
SET config=jsonb_set(config,'{new_account_tests}',
    (COALESCE(config->'new_account_tests','{}'::jsonb)-'model') || jsonb_build_object(
        'attribution',COALESCE((config->'new_account_tests'->>'attribution')::boolean,true),
        'pelican',COALESCE((config->'new_account_tests'->>'pelican')::boolean,true),
        'attribution_model',COALESCE(NULLIF(config->'new_account_tests'->>'attribution_model',''),NULLIF(config->'new_account_tests'->>'model',''),'gpt-6-astra'),
        'pelican_model',COALESCE(NULLIF(config->'new_account_tests'->>'pelican_model',''),NULLIF(NULLIF(config->'new_account_tests'->>'model',''),'gpt-6-astra'),'gpt-6.1-sol')
    )),
    version=version+1,updated_at=NOW();
