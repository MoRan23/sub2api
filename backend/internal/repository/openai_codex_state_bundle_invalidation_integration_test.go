//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexStateBundleInvalidationVersionSurvivesReplacement(t *testing.T) {
	ctx := context.Background()
	repo, key, candidate, _ := newCodexBoundProxyFixture(t)
	read := func() *service.CodexTurnStateRecord {
		t.Helper()
		record, err := repo.Get(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, record)
		record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
		return record
	}
	save := func(record *service.CodexTurnStateRecord) {
		t.Helper()
		saved, err := repo.SaveCAS(ctx, *record, record.Version)
		require.NoError(t, err)
		require.True(t, saved)
	}
	initialVersion := read().BundleInvalidationVersion
	require.Positive(t, initialVersion, "migration and new rows establish a nonzero revocation fence")
	save(candidate)
	first := read()
	require.Equal(t, initialVersion, first.BundleInvalidationVersion)
	first.EncryptedToken, first.EncryptedCookieBundle = "replacement-ticket", "replacement-cookies"
	first.IssuedAt = first.IssuedAt.Add(time.Second)
	first.BundleInvalidationVersion = 999 // The caller cannot assign the persisted fence.
	save(first)
	replacement := read()
	require.Equal(t, initialVersion, replacement.BundleInvalidationVersion, "normal replacement preserves frozen retries")
	clear := *replacement
	clear.EncryptedToken, clear.EncryptedCookieBundle = "", ""
	clear.ExpiresAt, clear.CookieBundleExpiresAt = time.Time{}, nil
	clear.BundleBinding = service.CodexTurnStateBundleBinding{}
	save(&clear)
	cleared := read()
	require.Equal(t, initialVersion+1, cleared.BundleInvalidationVersion)
	save(cleared)
	require.Equal(t, initialVersion+1, read().BundleInvalidationVersion, "repeated empty scheduling writes do not revoke again")
	// A new target after revocation must not restore the old fence (the ABA case).
	replacement.Version = read().Version
	replacement.BundleInvalidationVersion = initialVersion
	save(replacement)
	recovered := read()
	require.Equal(t, initialVersion+1, recovered.BundleInvalidationVersion)
	require.Equal(t, replacement.EncryptedToken, recovered.EncryptedToken)
}

func TestCodexStateBundleSchedulingSurvivesInvalidRoute(t *testing.T) {
	for _, mutation := range []string{"generation", "disabled", "expired"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			repo, key, candidate, proxy := newCodexBoundProxyFixture(t)
			candidate.BundleBinding.WireMode = "lite"
			saved, err := repo.SaveCAS(ctx, *candidate, candidate.Version)
			require.NoError(t, err)
			require.True(t, saved)
			switch mutation {
			case "generation":
				_, err = integrationDB.ExecContext(ctx, `UPDATE proxies SET host='changed.invalid',route_generation=route_generation+1 WHERE id=$1`, proxy.ID)
			case "disabled":
				_, err = integrationDB.ExecContext(ctx, `UPDATE proxies SET status='inactive' WHERE id=$1`, proxy.ID)
			case "expired":
				_, err = integrationDB.ExecContext(ctx, `UPDATE proxies SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, proxy.ID)
			}
			require.NoError(t, err)
			now := time.Now().UTC().Truncate(time.Microsecond)
			require.NoError(t, repo.MarkEligibleCollectionSent(ctx, key, now))
			reservation, err := repo.Get(ctx, key)
			require.NoError(t, err)
			reservation.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
			reservation.DemandReason, reservation.CollectionStatus, reservation.CollectionReason = "refresh", "collecting", "collecting"
			reservation.CollectorProxyID, reservation.LastCollectedAt = proxy.ID, now
			reservation.CollectorAttemptID = "00000000-0000-4000-8000-000000000009"
			reservation.NextCollectAt = now.Add(service.CodexTurnStateCollectTimeout)
			saved, err = repo.SaveCAS(ctx, *reservation, reservation.Version)
			require.NoError(t, err)
			require.True(t, saved, "an unusable cached route must not prevent collection of its replacement")
			stored, err := repo.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, reservation.EncryptedToken, stored.EncryptedToken)
			require.Equal(t, reservation.EncryptedCookieBundle, stored.EncryptedCookieBundle)
			require.Equal(t, reservation.BundleBinding, stored.BundleBinding)
			require.Equal(t, reservation.BundleInvalidationVersion, stored.BundleInvalidationVersion)
			require.Equal(t, reservation.CollectorAttemptID, stored.CollectorAttemptID)
			stored.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
			for _, changed := range []string{"token", "cookie", "expiry", "cookie_expiry", "issued", "shape", "mode", "binding"} {
				newPackage := *stored
				switch changed {
				case "token":
					newPackage.EncryptedToken += "-new"
				case "cookie":
					newPackage.EncryptedCookieBundle += "-new"
				case "expiry":
					newPackage.ExpiresAt = newPackage.ExpiresAt.Add(time.Second)
				case "cookie_expiry":
					newPackage.CookieBundleExpiresAt = &now
				case "issued":
					newPackage.IssuedAt = newPackage.IssuedAt.Add(time.Second)
				case "shape":
					newPackage.Shape = "extended"
				case "mode":
					newPackage.BundleBinding.WireMode = "responses"
				case "binding":
					newPackage.BundleBinding.ProxyRouteGeneration += 99
				}
				saved, err = repo.SaveCAS(ctx, newPackage, newPackage.Version)
				require.NoError(t, err)
				require.False(t, saved, changed+" cannot masquerade as a scheduling write")
			}
			// Once the selected managed route is usable, the completed collector
			// may publish a new pair under its actual current generation.
			_, err = integrationDB.ExecContext(ctx, `UPDATE proxies SET status='active',expires_at=NULL WHERE id=$1`, proxy.ID)
			require.NoError(t, err)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT route_generation FROM proxies WHERE id=$1`, proxy.ID).Scan(&stored.BundleBinding.ProxyRouteGeneration))
			stored.EncryptedToken, stored.EncryptedCookieBundle = "new-route-ticket", "new-route-cookies"
			stored.CollectionStatus, stored.CollectionReason, stored.CollectorAttemptID = "scheduled", "refresh", ""
			saved, err = repo.SaveCAS(ctx, *stored, stored.Version)
			require.NoError(t, err)
			require.True(t, saved)
			recovered, err := repo.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, stored.BundleBinding, recovered.BundleBinding)
			require.Equal(t, "new-route-ticket", recovered.EncryptedToken)
		})
	}
}
