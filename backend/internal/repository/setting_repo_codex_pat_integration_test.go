//go:build integration

package repository

import (
	"context"
	"net/http"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSettingRepositoryCodexPATConcurrentInstances(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	// Distinct repository/client instances share only PostgreSQL, not an
	// application mutex. Their concurrent transactions use separate connections.
	otherClient := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	first := &settingRepository{client: client}
	second := &settingRepository{client: otherClient}
	const markerKey = "test_codex_pat_atomic_marker"
	keys := append(append([]string{}, codexPATSettingsKeys...), markerKey)
	previous, err := first.GetMultiple(ctx, keys)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := client.Setting.Delete().Where(setting.KeyIn(keys...)).Exec(ctx)
		require.NoError(t, err)
		if len(previous) > 0 {
			// Preserve even a pre-existing invalid fixture exactly, rather than
			// normalizing it through the production guarded writer.
			require.NoError(t, setMultipleSettings(ctx, client, previous))
		}
	})

	for _, dependency := range codexPATSettingsKeys[1:] {
		for _, absent := range []bool{false, true} {
			for iteration := 0; iteration < 10; iteration++ {
				_, err := client.Setting.Delete().Where(setting.KeyIn(keys...)).Exec(ctx)
				require.NoError(t, err)
				if !absent {
					require.NoError(t, first.SetMultiple(ctx, map[string]string{
						service.SettingKeyEnableOpenAICodexPATContextManagement:     "false",
						service.SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
						service.SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
					}))
				}
				start := make(chan struct{})
				type result struct {
					marker string
					err    error
				}
				results := make(chan result, 2)
				go func() {
					<-start
					results <- result{"enabled", first.SetMultiple(ctx, map[string]string{
						service.SettingKeyEnableOpenAICodexPATContextManagement: "true", markerKey: "enabled",
					})}
				}()
				go func() {
					<-start
					results <- result{"disabled", second.SetMultiple(ctx, map[string]string{dependency: "false", markerKey: "disabled"})}
				}()
				close(start)
				winners := 0
				winnerMarker := ""
				for range 2 {
					result := <-results
					if result.err == nil {
						winners++
						winnerMarker = result.marker
					} else {
						require.Equal(t, http.StatusBadRequest, infraerrors.Code(result.err))
						require.Equal(t, "INVALID_CODEX_PAT_CONTEXT_MANAGEMENT", infraerrors.Reason(result.err))
					}
				}
				require.Equal(t, 1, winners)
				stored, err := first.GetMultiple(ctx, keys)
				require.NoError(t, err)
				require.NoError(t, service.ValidateOpenAICodexPATContextManagementValues(stored))
				require.Equal(t, winnerMarker, stored[markerKey], "the rejected transaction must not save other fields")
			}
		}
	}
}
