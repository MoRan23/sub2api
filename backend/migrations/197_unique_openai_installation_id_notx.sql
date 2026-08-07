CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_openai_oauth_pinned_installation_id
    ON accounts ((extra ->> 'openai_pinned_installation_id'))
    WHERE deleted_at IS NULL
      AND platform = 'openai'
      AND type = 'oauth'
      AND parent_account_id IS NULL;
