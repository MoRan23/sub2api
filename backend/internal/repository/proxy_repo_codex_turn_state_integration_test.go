//go:build integration

package repository

import (
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *ProxyRepoSuite) TestCodexCollectorProxyReferencesAndDeleteGuard() {
	p1 := s.mustCreateProxy(&service.Proxy{Name: "collector", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	p2 := s.mustCreateProxy(&service.Proxy{Name: "business", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive})
	insert := func(name string, businessID *int64, collector any, deleted bool) {
		encoded, err := json.Marshal(map[string]any{"codex_turn_state": map[string]any{"enabled": false, "collector_proxy_id": collector}})
		s.Require().NoError(err)
		var deletedAt any
		if deleted {
			deletedAt = time.Now()
		}
		_, err = s.tx.ExecContext(s.ctx, `INSERT INTO accounts(name, platform, type, proxy_id, extra, deleted_at)
			VALUES ($1, 'openai', 'oauth', $2, $3::jsonb, $4)`, name, businessID, string(encoded), deletedAt)
		s.Require().NoError(err)
	}
	insert("both-same", &p1.ID, p1.ID, false)
	insert("collector-only", nil, p1.ID, false)
	insert("split-routes", &p2.ID, p1.ID, false)
	insert("deleted", nil, p1.ID, true)
	insert("malformed-string", nil, "invalid", false)
	insert("oversized-number", nil, json.Number("9223372036854775808000"), false)
	insert("passive", nil, nil, false)

	count, err := s.repo.CountAccountsByProxyID(s.ctx, p1.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), count)
	items, err := s.repo.ListAccountSummariesByProxyID(s.ctx, p1.ID)
	s.Require().NoError(err)
	s.Require().Len(items, 3)
	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), counts[p1.ID], "same account/proxy pair counts once")
	s.Require().Equal(int64(1), counts[p2.ID])
	s.Require().ErrorIs(s.repo.Delete(s.ctx, p1.ID), service.ErrProxyInUse)
	_, err = s.repo.GetByID(s.ctx, p1.ID)
	s.Require().NoError(err, "collector reference protects the proxy from deletion")
}

func (s *ProxyRepoSuite) TestCodexCollectorExpiryDoesNotRewriteBusinessRoute() {
	now := time.Now()
	expired := now.Add(-time.Hour)
	collector := s.mustCreateProxy(&service.Proxy{Name: "expired-collector", Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: service.StatusActive, ExpiresAt: &expired, FallbackMode: service.FallbackModeDirect})
	business := s.mustCreateProxy(&service.Proxy{Name: "business-route", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive})
	extra, err := json.Marshal(map[string]any{"codex_turn_state": map[string]any{"enabled": true, "collector_proxy_id": collector.ID}})
	s.Require().NoError(err)
	var accountID int64
	s.Require().NoError(scanSingleRow(s.ctx, s.tx, `INSERT INTO accounts(name, platform, type, proxy_id, extra)
		VALUES ('collector-expiry-isolation', 'openai', 'oauth', $1, $2::jsonb) RETURNING id`, []any{business.ID, string(extra)}, &accountID))
	changed, err := s.repo.SweepExpiredProxies(s.ctx, now)
	s.Require().NoError(err)
	s.Require().Zero(changed)
	var businessID, collectorID int64
	s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT proxy_id, (extra #>> '{codex_turn_state,collector_proxy_id}')::bigint
		FROM accounts WHERE id=$1`, []any{accountID}, &businessID, &collectorID))
	s.Require().Equal(business.ID, businessID)
	s.Require().Equal(collector.ID, collectorID)
	stored, err := s.repo.GetByID(s.ctx, collector.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusExpired, stored.Status)
}
