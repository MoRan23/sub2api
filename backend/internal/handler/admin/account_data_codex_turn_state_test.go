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
	businessID, collectorID, alternateID, parentID := int64(11), int64(22), int64(33), int64(7)
	source.proxies = []service.Proxy{
		{ID: businessID, Name: "business", Protocol: "http", Host: "business.example", Port: 8080, Status: service.StatusActive},
		{ID: collectorID, Name: "collector", Protocol: "socks5", Host: "collector.example", Port: 1080, Status: service.StatusActive},
		{ID: alternateID, Name: "alternate", Protocol: "http", Host: "alternate.example", Port: 8080, Status: service.StatusActive},
		{ID: 44, Name: "unused", Protocol: "http", Host: "unused.example", Port: 8080, Status: service.StatusActive},
	}
	source.accounts = []service.Account{
		{ID: parentID, Name: "parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "oauth-credential"}, ProxyID: &businessID,
			Extra: map[string]any{
				"note": "keep", "codex_turn_state_generation": "must-not-leak-generation",
				"codex_turn_state_credential_epoch": "must-not-leak-credential-epoch",
				"codex_turn_state_runtime":          map[string]any{"token": "must-not-leak-token"},
				"codex_turn_state":                  map[string]any{"enabled": true, "account_type": "team_business", "collector_proxy_ids": []int64{alternateID, collectorID, businessID}, "use_ticket_proxy": false, "token": "must-not-leak-nested", "collector_attempt_id": "must-not-leak-attempt", "collector_extended_count": 2},
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
	require.Len(t, exported.Data.Proxies, 3, "business and collector references must not duplicate proxy records")
	require.Equal(t, 1, exported.Data.SkippedShadows)
	account := exported.Data.Accounts[0]
	require.Equal(t, map[string]any{"note": "keep"}, account.Extra)
	require.NotNil(t, account.CodexTurnState)
	require.True(t, account.CodexTurnState.Enabled)
	require.NotNil(t, account.CodexTurnState.UseTicketProxy)
	require.False(t, *account.CodexTurnState.UseTicketProxy)
	require.Nil(t, account.CodexTurnState.CollectorProxyID)
	require.Empty(t, account.CodexTurnState.CollectorProxyIDs)
	require.Nil(t, account.CodexTurnStateProxyKey)
	require.NotNil(t, account.CodexTurnStateProxyKeys)
	require.Len(t, *account.CodexTurnStateProxyKeys, 3)
	require.Contains(t, (*account.CodexTurnStateProxyKeys)[0], "alternate.example")
	require.Contains(t, (*account.CodexTurnStateProxyKeys)[1], "collector.example")
	require.Contains(t, (*account.CodexTurnStateProxyKeys)[2], "business.example")
	require.NotContains(t, rec.Body.String(), "collector_extended_count")
	require.Equal(t, []int64{alternateID, collectorID, businessID}, source.accounts[0].Extra["codex_turn_state"].(map[string]any)["collector_proxy_ids"], "export must not mutate live configuration")

	destinationRouter, destination := setupAccountDataRouter()
	destination.proxies = append([]service.Proxy(nil), source.proxies...)
	destination.proxies[0].ID = 111
	destination.proxies[1].ID = 222
	destination.proxies[2].ID = 333
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
	require.Equal(t, []int64{333, 222, 111}, created.CodexTurnState.CollectorProxyIDs)
	require.Nil(t, created.CodexTurnState.CollectorProxyID)
	require.Equal(t, "team_business", created.CodexTurnState.AccountType)
	require.True(t, created.CodexTurnState.Enabled)
	require.NotNil(t, created.CodexTurnState.UseTicketProxy)
	require.False(t, *created.CodexTurnState.UseTicketProxy)
	require.Equal(t, map[string]any{"note": "keep"}, created.Extra)
}

func TestImportCodexTurnStateRejectsUnmappedLocalIDs(t *testing.T) {
	id, key, blank := int64(42), "missing-key", ""
	for name, item := range map[string]DataAccount{
		"local ID":             {CodexTurnState: &service.CodexTurnStateConfig{CollectorProxyID: &id}},
		"local IDs":            {CodexTurnState: &service.CodexTurnStateConfig{CollectorProxyIDs: []int64{id}}},
		"legacy local ID":      {Extra: map[string]any{"codex_turn_state": map[string]any{"collector_proxy_id": id}}},
		"missing mapping":      {CodexTurnState: &service.CodexTurnStateConfig{}, CodexTurnStateProxyKey: &key},
		"empty mapping":        {CodexTurnState: &service.CodexTurnStateConfig{}, CodexTurnStateProxyKey: &blank},
		"missing config":       {CodexTurnStateProxyKey: &key},
		"malformed legacy":     {Extra: map[string]any{"codex_turn_state": map[string]any{"collector_proxy_id": "42"}}},
		"missing list config":  {CodexTurnStateProxyKeys: stringSlicePointer("key")},
		"missing list mapping": {CodexTurnState: &service.CodexTurnStateConfig{}, CodexTurnStateProxyKeys: stringSlicePointer("key")},
		"empty list mapping":   {CodexTurnState: &service.CodexTurnStateConfig{}, CodexTurnStateProxyKeys: stringSlicePointer("")},
		"legacy local IDs":     {Extra: map[string]any{"codex_turn_state": map[string]any{"collector_proxy_ids": []int64{id}}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := importCodexTurnStateConfig(item, nil)
			require.Error(t, err)
		})
	}
	config, err := importCodexTurnStateConfig(DataAccount{Extra: map[string]any{"codex_turn_state": map[string]any{"enabled": true}}}, nil)
	require.NoError(t, err)
	require.True(t, config.Enabled)
	require.True(t, service.CodexTurnStateUseTicketProxy(*config), "old backups retain the previous routing behavior")
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
	require.NotNil(t, exported.Data.Accounts[0].CodexTurnStateProxyKeys)
	require.Len(t, *exported.Data.Accounts[0].CodexTurnStateProxyKeys, 1)
	require.Nil(t, exported.Data.Accounts[0].CodexTurnStateProxyKey)

	destinationRouter, destination := setupAccountDataRouter()
	body, err := json.Marshal(DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	destinationRouter.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "codex_turn_state_proxy_keys entry not found")
	require.Empty(t, destination.createdAccounts, "never silently turn a configured collector into passive mode")
}

func stringSlicePointer(values ...string) *[]string {
	if values == nil {
		values = []string{}
	}
	return &values
}

func TestImportCodexTurnStatePortableListPrecedence(t *testing.T) {
	legacyKey, localID := "legacy", int64(900)
	mapping := map[string]int64{"legacy": 11, "first": 22, "second": 33, "alias": 22}
	for _, tc := range []struct {
		name      string
		keys      *[]string
		want      []int64
		wantError bool
	}{
		{name: "legacy key", want: []int64{11}},
		{name: "ordered list overrides legacy", keys: stringSlicePointer("second", "first"), want: []int64{33, 22}},
		{name: "explicit empty overrides legacy", keys: stringSlicePointer(), want: []int64{}},
		{name: "partial mapping fails entire account", keys: stringSlicePointer("first", "missing"), wantError: true},
		{name: "duplicate key", keys: stringSlicePointer("first", "first"), wantError: true},
		{name: "duplicate destination ID", keys: stringSlicePointer("first", "alias"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &service.CodexTurnStateConfig{Enabled: true, CollectorProxyID: &localID, CollectorProxyIDs: []int64{901, 902}}
			actual, err := importCodexTurnStateConfig(DataAccount{CodexTurnState: input, CodexTurnStateProxyKeys: tc.keys, CodexTurnStateProxyKey: &legacyKey}, mapping)
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, actual)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, actual.CollectorProxyIDs)
				require.Nil(t, actual.CollectorProxyID)
			}
			require.Equal(t, []int64{901, 902}, input.CollectorProxyIDs, "import must not mutate source arrays")
			require.Equal(t, &localID, input.CollectorProxyID)
		})
	}
}

func TestAccountDataCodexTurnStateEmptyPortableListSurvivesJSON(t *testing.T) {
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": true, "collector_proxy_ids": []int64{}}}}
	config, keys, err := exportCodexTurnStateConfig(account, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(DataAccount{CodexTurnState: config, CodexTurnStateProxyKeys: keys})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"codex_turn_state_proxy_keys":[]`)
	var decoded DataAccount
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	legacy := "must-not-resurrect"
	decoded.CodexTurnStateProxyKey = &legacy
	restored, err := importCodexTurnStateConfig(decoded, map[string]int64{legacy: 99})
	require.NoError(t, err)
	require.Equal(t, []int64{}, restored.CollectorProxyIDs)
}
