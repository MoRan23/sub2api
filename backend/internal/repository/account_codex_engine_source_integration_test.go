//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"maps"
	"testing"
)

func TestCodexEngineSourceIntegrationRejectsABA(t *testing.T) {
	for _, path := range []string{"full", "credentials", "bulk", "extra"} {
		for _, field := range []string{"base_url", "api_key", "mode", "proxy"} {
			if (path == "credentials" && (field == "mode" || field == "proxy")) || (path == "extra" && field != "mode") {
				continue
			}
			t.Run(path+"/"+field, func(t *testing.T) {
				repo, a := engineCapacityFixture(t, nil)
				changed, err := repo.ApplyCodexEngineCapacity(t.Context(), a, 0)
				require.NoError(t, err)
				require.True(t, changed)
				a, err = repo.GetByID(t.Context(), a.ID)
				require.NoError(t, err)
				changed, err = repo.ApplyCodexEngineModels(t.Context(), a, []string{"original"})
				require.NoError(t, err)
				require.True(t, changed)
				a, err = repo.GetByID(t.Context(), a.ID)
				require.NoError(t, err)
				var proxyID int64
				if field == "proxy" {
					p := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "source-aba", Host: "source.invalid", Port: 8080})
					proxyID = p.ID
					t.Cleanup(func() {
						_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", a.ID)
						_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE id=$1", proxyID)
					})
				}
				for _, away := range []bool{true, false} {
					current, err := repo.GetByID(t.Context(), a.ID)
					require.NoError(t, err)
					creds := maps.Clone(current.Credentials)
					extra := map[string]any{}
					var proxy *int64
					switch field {
					case "base_url", "api_key":
						if away {
							creds[field] = "replacement"
						} else {
							creds[field] = a.Credentials[field]
						}
					case "mode":
						value := "codex_engine"
						if away {
							value = "generic"
						}
						extra[service.OpenAIAPIKeyModeExtraKey] = value
					case "proxy":
						value := int64(0)
						if away {
							value = proxyID
						}
						proxy = &value
					}
					switch path {
					case "full":
						current.Credentials = creds
						for k, v := range extra {
							current.Extra[k] = v
						}
						if proxy != nil {
							current.ProxyID = nil
							if *proxy != 0 {
								current.ProxyID = proxy
							}
						}
						require.NoError(t, repo.Update(t.Context(), current))
					case "credentials":
						require.NoError(t, repo.UpdateCredentials(t.Context(), a.ID, creds))
					case "bulk":
						_, err = repo.BulkUpdate(t.Context(), []int64{a.ID}, service.AccountBulkUpdate{Credentials: creds, Extra: extra, ProxyID: proxy})
						require.NoError(t, err)
					case "extra":
						require.NoError(t, repo.UpdateExtra(t.Context(), a.ID, extra))
					}
				}
				before := engineCapacityState(t, a.ID)
				changed, err = repo.ApplyCodexEngineCapacity(t.Context(), a, 30)
				require.NoError(t, err)
				require.False(t, changed, "capacity must reject old source even after it returns to A")
				changed, err = repo.ApplyCodexEngineModels(t.Context(), a, []string{"late"})
				require.NoError(t, err)
				require.False(t, changed, "models must reject old source even after it returns to A")
				require.Equal(t, before, engineCapacityState(t, a.ID))
				fresh, err := repo.GetByID(t.Context(), a.ID)
				require.NoError(t, err)
				changed, err = repo.ApplyCodexEngineModels(t.Context(), fresh, []string{"fresh"})
				require.NoError(t, err)
				require.True(t, changed)
				changed, err = repo.ApplyCodexEngineCapacity(t.Context(), fresh, 30)
				require.NoError(t, err)
				require.True(t, changed)
				if field != "mode" {
					require.True(t, engineCapacityState(t, a.ID).Schedulable, "source fence preserves capacity pause ownership")
				}
			})
		}
	}
}

func TestCodexEngineSourceIntegrationProxyEndpointABA(t *testing.T) {
	repo, a := engineCapacityFixture(t, nil)
	p := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "endpoint-aba", Host: "original.invalid", Port: 8080})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", a.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE id=$1", p.ID)
	})
	a.ProxyID = &p.ID
	require.NoError(t, repo.Update(t.Context(), a))
	a, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	proxyRepo := newProxyRepositoryWithSQL(integrationEntClient, integrationDB)
	p.Host = "temporary.invalid"
	require.NoError(t, proxyRepo.Update(t.Context(), p))
	p.Host = "original.invalid"
	require.NoError(t, proxyRepo.Update(t.Context(), p))
	changed, err := repo.ApplyCodexEngineModels(t.Context(), a, []string{"late"})
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = repo.ApplyCodexEngineCapacity(t.Context(), a, 20)
	require.NoError(t, err)
	require.False(t, changed)
	fresh, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	changed, err = repo.ApplyCodexEngineCapacity(t.Context(), fresh, 20)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = repo.ApplyCodexEngineModels(t.Context(), fresh, []string{"fresh"})
	require.NoError(t, err)
	require.True(t, changed)
}
