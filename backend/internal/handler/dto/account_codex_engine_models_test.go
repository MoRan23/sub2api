package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountCodexEngineModelsSummary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		known bool
		count int
		mode  string
		want  *CodexEngineModels
	}{
		{"empty catalog", true, 0, "codex_engine", &CodexEngineModels{Synced: true, ModelCount: 0}},
		{"populated catalog", true, 2, "codex_engine", &CodexEngineModels{Synced: true, ModelCount: 2}},
		{"not observed", false, 0, "codex_engine", nil},
		{"ordinary key", true, 0, "generic", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "secret-key"},
				Extra: map[string]any{"openai_api_key_mode": tc.mode, service.CodexEngineModelsSyncExtraKey: map[string]any{
					"known": tc.known, "model_count": tc.count, "identity": "private-source-identity", "observation_id": "private-observation",
				}},
			}
			account.Extra[service.CodexEngineSourceRevisionExtraKey] = "private-source-revision"
			full := AccountFromService(account)
			lite := AccountListItemFromAccount(full)
			require.Equal(t, tc.want, full.CodexEngineModels)
			require.Equal(t, tc.want, lite.CodexEngineModels)
			for _, dto := range []any{full, lite} {
				raw, err := json.Marshal(dto)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "secret-key")
				require.NotContains(t, string(raw), "private-source-identity")
				require.NotContains(t, string(raw), "private-observation")
				require.NotContains(t, string(raw), service.CodexEngineModelsSyncExtraKey)
				require.NotContains(t, string(raw), service.CodexEngineSourceRevisionExtraKey)
				require.NotContains(t, string(raw), "private-source-revision")
			}
			require.Contains(t, account.Extra, service.CodexEngineModelsSyncExtraKey)
		})
	}
}
