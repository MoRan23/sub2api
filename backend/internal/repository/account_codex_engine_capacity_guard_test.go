package repository

import (
	"context"
	"maps"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPreserveCodexEngineCapacityLocked(t *testing.T) {
	for _, tc := range []struct {
		name                string
		change              func(*service.Account)
		limit               int
		preserved           bool
		preserveConcurrency bool
		needsQuery          bool
	}{
		{name: "stale positive snapshot", limit: 20, preserved: true, preserveConcurrency: true, needsQuery: true},
		{name: "stale zero snapshot", limit: 0, preserved: true, preserveConcurrency: true, needsQuery: true},
		{name: "name edit", limit: 20, change: func(a *service.Account) { a.Name = "new name" }, preserved: true, preserveConcurrency: true, needsQuery: true},
		{name: "raw base url changes", change: func(a *service.Account) { a.Credentials["base_url"] = "https://engine.test/" }, preserved: true, needsQuery: true},
		{name: "key changes", change: func(a *service.Account) { a.Credentials["api_key"] = "next-key" }, preserved: true, needsQuery: true},
		{name: "other credential changes", change: func(a *service.Account) { a.Credentials["custom"] = true }, preserved: true, preserveConcurrency: true, needsQuery: true},
		{name: "proxy changes", change: func(a *service.Account) { id := int64(9); a.ProxyID = &id }, preserved: true, needsQuery: true},
		{name: "mode changes", change: func(a *service.Account) { a.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic" }},
		{name: "platform changes", change: func(a *service.Account) { a.Platform = service.PlatformAnthropic }},
		{name: "type changes", change: func(a *service.Account) { a.Type = service.AccountTypeOAuth }},
		{name: "becomes shadow", change: func(a *service.Account) { id := int64(1); a.ParentAccountID = &id }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			current := &service.Account{ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://engine.test", "api_key": "key"},
				Extra:       map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}}
			current.Extra[service.CodexEngineCapacitySyncExtraKey] = map[string]any{
				"limit": tc.limit, "identity": service.CodexEngineCapacityIdentity(current),
			}
			target := *current
			target.Concurrency = 2
			target.Credentials = maps.Clone(current.Credentials)
			target.Extra = maps.Clone(current.Extra)
			target.Extra[service.CodexEngineCapacitySyncExtraKey] = map[string]any{"limit": 99, "identity": "injected"}
			if tc.change != nil {
				tc.change(&target)
			}
			if tc.needsQuery {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT proxy_id, concurrency FROM accounts WHERE id = $1 AND deleted_at IS NULL")).
					WithArgs(int64(27)).WillReturnRows(sqlmock.NewRows([]string{"proxy_id", "concurrency"}).AddRow(nil, 30))
			}
			require.NoError(t, preserveCodexEngineCapacityLocked(context.Background(), client, current, &target))
			if tc.preserved {
				require.Equal(t, current.Extra[service.CodexEngineCapacitySyncExtraKey], target.Extra[service.CodexEngineCapacitySyncExtraKey])
			} else {
				require.NotContains(t, target.Extra, service.CodexEngineCapacitySyncExtraKey)
			}
			if tc.preserveConcurrency {
				require.Equal(t, 30, target.Concurrency)
			} else {
				require.Equal(t, 2, target.Concurrency)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAccountConfigurationExtraPatchRejectsCapacitySnapshot(t *testing.T) {
	input := map[string]any{
		service.CodexEngineCapacitySyncExtraKey: map[string]any{"limit": 0},
		"unrelated":                             true,
	}
	filtered := accountConfigurationExtraPatch(context.Background(), []int64{27}, input)
	require.NotContains(t, filtered, service.CodexEngineCapacitySyncExtraKey)
	require.Equal(t, true, filtered["unrelated"])
	require.Contains(t, input, service.CodexEngineCapacitySyncExtraKey)
}
