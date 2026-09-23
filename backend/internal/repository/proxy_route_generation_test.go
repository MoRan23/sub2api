package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyUpdateRouteGeneration(t *testing.T) {
	cases := []struct {
		name       string
		change     func(*service.Proxy)
		bump       bool
		invalidate bool
	}{
		{name: "unchanged", change: func(*service.Proxy) {}},
		{name: "protocol", change: func(p *service.Proxy) { p.Protocol = "socks5" }, bump: true, invalidate: true},
		{name: "host", change: func(p *service.Proxy) { p.Host = "new.example" }, bump: true, invalidate: true},
		{name: "port", change: func(p *service.Proxy) { p.Port++ }, bump: true, invalidate: true},
		{name: "username", change: func(p *service.Proxy) { p.Username = "new-user" }, bump: true, invalidate: true},
		{name: "password", change: func(p *service.Proxy) { p.Password = "new-pass" }, bump: true, invalidate: true},
		{name: "clear credentials", change: func(p *service.Proxy) { p.Username, p.Password = "", "" }, bump: true, invalidate: true},
		{name: "name", change: func(p *service.Proxy) { p.Name = "renamed" }},
		{name: "status", change: func(p *service.Proxy) { p.Status = service.StatusDisabled }, invalidate: true},
		{name: "expiry", change: func(p *service.Proxy) { expiry := time.Now().Add(time.Hour); p.ExpiresAt = &expiry }},
		{name: "fallback", change: func(p *service.Proxy) { p.FallbackMode = service.FallbackModeDirect }},
		{name: "expiry warning", change: func(p *service.Proxy) { p.ExpiryWarnDays = 30 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var updateSQL string
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expected, actual string) error {
				if strings.HasPrefix(actual, `UPDATE "proxies"`) && strings.Contains(actual, `"name"`) {
					updateSQL = actual
				}
				return sqlmock.QueryMatcherRegexp.Match(expected, actual)
			})))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })

			input := &service.Proxy{ID: 9, Name: "proxy", Protocol: "http", Host: "same.example", Port: 8080,
				Username: "user", Password: "pass", Status: service.StatusActive, RouteGeneration: 999,
				FallbackMode: service.FallbackModeNone, ExpiryWarnDays: 7}
			tc.change(input)
			generation := int64(12)
			if tc.bump {
				generation++
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR NO KEY UPDATE")).
				WithArgs(input.ID).
				WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
					AddRow("http", "same.example", 8080, "user", "pass", service.StatusActive))
			mock.ExpectExec(`UPDATE "proxies" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
			// Clearing this proxy's directed backup must not clear other proxies'
			// references to it. An unexpected second UPDATE fails the mock.
			mock.ExpectQuery(`(?s)SELECT .* FROM "proxies" WHERE "id" = \$1`).WithArgs(input.ID).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "created_at", "updated_at", "deleted_at", "name", "protocol", "host", "port",
					"username", "password", "status", "route_generation", "expires_at", "fallback_mode", "backup_proxy_id", "expiry_warn_days",
				}).AddRow(input.ID, time.Now(), time.Now(), nil, input.Name, input.Protocol, input.Host, input.Port,
					input.Username, input.Password, input.Status, generation, input.ExpiresAt, input.FallbackMode, nil, input.ExpiryWarnDays))
			if tc.invalidate {
				mock.ExpectQuery(`(?s)UPDATE accounts.*type = 'apikey'.*RETURNING id`).WithArgs(input.ID).
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			mock.ExpectCommit()

			require.NoError(t, newProxyRepositoryWithSQL(client, db).Update(context.Background(), input))
			require.Equal(t, generation, input.RouteGeneration, "callers receive the stored generation")
			if tc.bump {
				require.Regexp(t, `"route_generation".*\+`, updateSQL, "generation increments in the proxy UPDATE")
			} else {
				require.NotContains(t, updateSQL, `"route_generation"`, "caller-supplied generation cannot overwrite storage")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestProxyEntityMappingIncludesRouteGeneration(t *testing.T) {
	entity := &dbent.Proxy{ID: 9, RouteGeneration: 42}
	require.Equal(t, int64(42), proxyEntityToService(entity).RouteGeneration)
	input := &service.Proxy{RouteGeneration: 1}
	applyProxyEntityToService(input, entity)
	require.Equal(t, int64(42), input.RouteGeneration)
}
