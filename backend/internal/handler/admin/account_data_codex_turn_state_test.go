package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDataCodexTurnStatePortableRoundTrip(t *testing.T) {
	router, source := setupAccountDataRouter()
	businessID, collectorID, parentID := int64(11), int64(22), int64(7)
	source.proxies = []service.Proxy{
		{ID: businessID, Name: "business", Protocol: "http", Host: "business.example", Port: 8080, Status: service.StatusActive},
		{ID: collectorID, Name: "collector", Protocol: "socks5", Host: "collector.example", Port: 1080, Status: service.StatusActive},
		{ID: 33, Name: "unused", Protocol: "http", Host: "unused.example", Port: 8080, Status: service.StatusActive},
	}
	source.accounts = []service.Account{
		{ID: parentID, Name: "parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "oauth-credential"}, ProxyID: &businessID,
			Extra: map[string]any{
				"note": "keep", "codex_turn_state_generation": "must-not-leak-generation",
				"codex_turn_state_credential_epoch": "must-not-leak-credential-epoch",
				"codex_turn_state_runtime":          map[string]any{"token": "must-not-leak-token"},
				"codex_turn_state":                  map[string]any{"enabled": true, "account_type": "team_business", "collector_proxy_id": collectorID, "token": "must-not-leak-nested"},
			}},
		{ID: 8, Name: "shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ParentAccountID: &parentID},
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "must-not-leak")
	var exported struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
	require.Len(t, exported.Data.Accounts, 1)
	require.Len(t, exported.Data.Proxies, 2)
	require.Equal(t, 1, exported.Data.SkippedShadows)
	account := exported.Data.Accounts[0]
	require.Equal(t, map[string]any{"note": "keep"}, account.Extra)
	require.NotNil(t, account.CodexTurnState)
	require.True(t, account.CodexTurnState.Enabled)
	require.Nil(t, account.CodexTurnState.CollectorProxyID)
	require.NotNil(t, account.CodexTurnStateProxyKey)
	require.Contains(t, *account.CodexTurnStateProxyKey, "collector.example")
	require.Equal(t, collectorID, source.accounts[0].Extra["codex_turn_state"].(map[string]any)["collector_proxy_id"], "export must not mutate live configuration")

	destinationRouter, destination := setupAccountDataRouter()
	destination.proxies = append([]service.Proxy(nil), source.proxies...)
	destination.proxies[0].ID = 111
	destination.proxies[1].ID = 222
	body, err := json.Marshal(DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	destinationRouter.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, destination.createdAccounts, 1, rec.Body.String())
	created := destination.createdAccounts[0]
	require.Equal(t, int64(111), *created.ProxyID)
	require.Equal(t, int64(222), *created.CodexTurnState.CollectorProxyID)
	require.Equal(t, "team_business", created.CodexTurnState.AccountType)
	require.True(t, created.CodexTurnState.Enabled)
	require.Equal(t, map[string]any{"note": "keep"}, created.Extra)
}

func TestImportCodexTurnStateRejectsUnmappedLocalIDs(t *testing.T) {
	id, key, blank := int64(42), "missing-key", ""
	for name, item := range map[string]DataAccount{
		"local ID":         {CodexTurnState: &service.CodexTurnStateConfig{CollectorProxyID: &id}},
		"legacy local ID":  {Extra: map[string]any{"codex_turn_state": map[string]any{"collector_proxy_id": id}}},
		"missing mapping":  {CodexTurnState: &service.CodexTurnStateConfig{}, CodexTurnStateProxyKey: &key},
		"empty mapping":    {CodexTurnState: &service.CodexTurnStateConfig{}, CodexTurnStateProxyKey: &blank},
		"missing config":   {CodexTurnStateProxyKey: &key},
		"malformed legacy": {Extra: map[string]any{"codex_turn_state": map[string]any{"collector_proxy_id": "42"}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := importCodexTurnStateConfig(item, nil)
			require.Error(t, err)
		})
	}
	config, err := importCodexTurnStateConfig(DataAccount{Extra: map[string]any{"codex_turn_state": map[string]any{"enabled": true}}}, nil)
	require.NoError(t, err)
	require.True(t, config.Enabled)
	require.Equal(t, "auto", config.AccountType)
	require.Nil(t, config.CollectorProxyID, "null collector stays passive")
	config, err = importCodexTurnStateConfig(DataAccount{}, nil)
	require.NoError(t, err)
	require.Nil(t, config, "old backups retain default-off behavior")
}

func TestAccountDataCodexTurnStateOmittedProxiesRetainPortableReference(t *testing.T) {
	router, source := setupAccountDataRouter()
	id := int64(22)
	source.proxies = []service.Proxy{{ID: id, Name: "collector", Protocol: "http", Host: "collector.example", Port: 8080, Status: service.StatusActive}}
	source.accounts = []service.Account{{ID: 7, Name: "parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-credential"},
		Extra:       map[string]any{"codex_turn_state": map[string]any{"enabled": true, "account_type": "auto", "collector_proxy_id": id}},
	}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var exported struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
	require.Empty(t, exported.Data.Proxies)
	require.NotNil(t, exported.Data.Accounts[0].CodexTurnStateProxyKey)

	destinationRouter, destination := setupAccountDataRouter()
	body, err := json.Marshal(DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	destinationRouter.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "codex_turn_state_proxy_key not found")
	require.Empty(t, destination.createdAccounts, "never silently turn a configured collector into passive mode")
}
