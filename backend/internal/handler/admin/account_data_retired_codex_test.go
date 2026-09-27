package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOldBackupRetiredCodexConfigurationIsIgnored(t *testing.T) {
	// Old collector references, including unavailable local proxy IDs, must no
	// longer prevent importing an otherwise valid account.
	var item DataAccount
	require.NoError(t, json.Unmarshal([]byte(`{
		"name":"legacy","platform":"openai","type":"apikey",
		"credentials":{"api_key":"synthetic"},
		"codex_turn_state":{"enabled":true,"collector_proxy_ids":[99999]},
		"codex_turn_state_proxy_keys":["missing-proxy"],
		"extra":{"note":"keep","codex_turn_state":{"enabled":true},
			"codex_turn_state_token":"old-secret","codex_turn_state_generation":"old-generation"}
	}`), &item))
	require.NoError(t, validateDataAccount(item))
	account := &service.Account{Platform: item.Platform, Type: item.Type, Extra: item.Extra}
	item.Extra = portableOpenAIOAuthExtra(account)
	require.Equal(t, map[string]any{"note": "keep"}, item.Extra)
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "codex_turn_state")
	require.NotContains(t, string(encoded), "old-secret")
}
