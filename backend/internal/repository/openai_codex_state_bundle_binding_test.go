package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexStateBundleProxyFence(t *testing.T) {
	for _, test := range []struct {
		name    string
		binding service.CodexTurnStateBundleBinding
		found   bool
		query   bool
		want    bool
	}{
		{name: "legacy is unbound"},
		{name: "compact rejected", binding: service.CodexTurnStateBundleBinding{WireMode: "compact", EgressKind: "direct"}},
		{name: "direct responses", binding: service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct"}, want: true},
		{name: "direct lite", binding: service.CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "direct"}, want: true},
		{name: "invalid direct proxy", binding: service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct", ProxyID: 9}},
		{name: "missing generation", binding: service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "proxy", ProxyID: 9}},
		{name: "live proxy", binding: service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "proxy", ProxyID: 9, ProxyRouteGeneration: 3}, query: true, found: true, want: true},
		{name: "stale disabled expired or deleted proxy", binding: service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "proxy", ProxyID: 9, ProxyRouteGeneration: 3}, query: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			tx, err := db.Begin()
			require.NoError(t, err)
			if test.query {
				query := mock.ExpectQuery(`(?s)SELECT id FROM proxies WHERE id=\$1 AND route_generation=\$2\s+AND deleted_at IS NULL AND status='active' AND \(expires_at IS NULL OR expires_at>NOW\(\)\) FOR SHARE`).WithArgs(int64(9), int64(3))
				if test.found {
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
				} else {
					query.WillReturnError(sql.ErrNoRows)
				}
			}
			ok, err := lockCodexStateBundleProxy(context.Background(), tx, service.CodexTurnStateRecord{EncryptedToken: "ticket", EncryptedCookieBundle: "cookies", BundleBinding: test.binding})
			require.NoError(t, err)
			require.Equal(t, test.want, ok)
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCodexStateBundleEgressAllowlist(t *testing.T) {
	for _, test := range []struct {
		name    string
		proxyID any
		extra   string
		binding service.CodexTurnStateBundleBinding
		want    bool
	}{
		{name: "original direct", extra: `{}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "direct"}, want: true},
		{name: "fallback direct forbidden", proxyID: int64(9), extra: `{}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "direct"}},
		{name: "business proxy", proxyID: int64(9), extra: `{}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "proxy", ProxyID: 9}, want: true},
		{name: "collector allowlist", extra: `{"codex_turn_state":{"collector_proxy_ids":[9,10]}}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "proxy", ProxyID: 10}, want: true},
		{name: "removed collector", extra: `{"codex_turn_state":{"collector_proxy_ids":[10]}}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "proxy", ProxyID: 9}},
		{name: "legacy collector", extra: `{"codex_turn_state":{"collector_proxy_id":9}}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "proxy", ProxyID: 9}, want: true},
		{name: "empty list overrides legacy", extra: `{"codex_turn_state":{"collector_proxy_ids":[],"collector_proxy_id":9}}`, binding: service.CodexTurnStateBundleBinding{EgressKind: "proxy", ProxyID: 9}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			tx, err := db.Begin()
			require.NoError(t, err)
			mock.ExpectQuery(`SELECT proxy_id, extra FROM accounts WHERE id=\$1`).WithArgs(int64(17)).
				WillReturnRows(sqlmock.NewRows([]string{"proxy_id", "extra"}).AddRow(test.proxyID, test.extra))
			ok, err := codexStateBundleEgressAllowed(context.Background(), tx, service.CodexTurnStateRecord{OwnerAccountID: 17, EncryptedToken: "ticket", EncryptedCookieBundle: "cookies", BundleBinding: test.binding})
			require.NoError(t, err)
			require.Equal(t, test.want, ok)
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
