package service

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateMultiProxyConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		want    []int64
		invalid bool
	}{
		{"legacy", `{"collector_proxy_id":7}`, []int64{7}, false},
		{"ordered", `{"collector_proxy_ids":[9,7]}`, []int64{9, 7}, false},
		{"array wins", `{"collector_proxy_ids":[9],"collector_proxy_id":7}`, []int64{9}, false},
		{"empty wins", `{"collector_proxy_ids":[],"collector_proxy_id":7}`, []int64{}, false},
		{"absent", `{}`, []int64{}, false},
		{"duplicate", `{"collector_proxy_ids":[7,7]}`, nil, true},
		{"zero", `{"collector_proxy_ids":[0]}`, nil, true},
		{"negative", `{"collector_proxy_ids":[-1]}`, nil, true},
		{"fraction", `{"collector_proxy_ids":[1.5]}`, nil, true},
		{"string", `{"collector_proxy_ids":["7"]}`, nil, true},
		{"null does not restore legacy", `{"collector_proxy_ids":null,"collector_proxy_id":7}`, nil, true},
		{"object does not restore legacy", `{"collector_proxy_ids":{},"collector_proxy_id":7}`, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cfg CodexTurnStateConfig
			err := json.Unmarshal([]byte(test.body), &cfg)
			cfg.AccountType = "auto"
			if err == nil {
				err = ValidateCodexTurnStateConfig(codexConfigAccount(t), &cfg)
			}
			if test.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, CodexTurnStateCollectorProxyIDs(cfg))
			stored := codexTurnStateConfigMap(cfg)
			require.NotContains(t, stored, "collector_proxy_id")
			require.Equal(t, test.want, stored["collector_proxy_ids"])
		})
	}
}

func TestCodexTurnStateMultiProxyMalformedStoredListFailsClosed(t *testing.T) {
	for _, body := range []string{
		`{"enabled":true,"account_type":"personal","collector_proxy_ids":null,"collector_proxy_id":7}`,
		`{"enabled":true,"account_type":"personal","collector_proxy_ids":[7,1.5],"collector_proxy_id":7}`,
		`{"enabled":true,"account_type":"personal","collector_proxy_ids":{},"collector_proxy_id":7}`,
	} {
		var raw map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &raw))
		account := codexConfigAccount(t)
		account.Extra[CodexTurnStateExtraKey] = raw
		cfg := CodexTurnStateConfigForAccount(account)
		require.False(t, cfg.Enabled)
		require.Empty(t, CodexTurnStateCollectorProxyIDs(cfg))
		require.Nil(t, cfg.CollectorProxyID)
	}
}

func TestCodexTurnStateMultiProxyJSONPreservesOmittedAndEmptyLists(t *testing.T) {
	for _, cfg := range []CodexTurnStateConfig{
		{CollectorProxyID: new(int64(7))},
		{CollectorProxyIDs: []int64{}, CollectorProxyID: new(int64(7))},
	} {
		encoded, err := json.Marshal(cfg)
		require.NoError(t, err)
		var decoded CodexTurnStateConfig
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.Equal(t, cfg.CollectorProxyIDs, decoded.CollectorProxyIDs)
		require.Equal(t, CodexTurnStateCollectorProxyIDs(cfg), CodexTurnStateCollectorProxyIDs(decoded))
	}
}

func TestCodexTurnStateMultiProxyIntentCopiesArrays(t *testing.T) {
	cfg := CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyIDs: []int64{8, 9}}
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, nil, nil, &cfg)
	cfg.CollectorProxyIDs[0] = 999
	first := AccountConfigurationIntentFromContext(ctx, 7)
	require.Equal(t, []int64{8, 9}, first.CodexTurnState.CollectorProxyIDs)
	first.CodexTurnState.CollectorProxyIDs[0] = 888
	require.Equal(t, []int64{8, 9}, AccountConfigurationIntentFromContext(ctx, 7).CodexTurnState.CollectorProxyIDs)
	ids := CodexTurnStateCollectorProxyIDs(*first.CodexTurnState)
	ids[1] = 777
	require.Equal(t, int64(9), first.CodexTurnState.CollectorProxyIDs[1])
	clear := CodexTurnStateConfig{CollectorProxyIDs: []int64{}}
	ctx = withAccountConfigurationIntent(context.Background(), []int64{7}, nil, nil, &clear)
	require.NotNil(t, AccountConfigurationIntentFromContext(ctx, 7).CodexTurnState.CollectorProxyIDs)
}

func TestCodexTurnStateMultiProxyLegacyCannotCollapseCurrentList(t *testing.T) {
	current := codexConfigAccount(t)
	current.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyIDs: []int64{8, 9}})
	target := *current
	legacy := CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyID: new(int64(8))}
	require.Error(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &legacy}))
	legacy.CollectorProxyID = nil
	require.Error(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &legacy}))
	legacy.CollectorProxyIDs = []int64{}
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &legacy}))
	require.Empty(t, CodexTurnStateCollectorProxyIDs(CodexTurnStateConfigForAccount(&target)))
	stale := *current
	stale.Extra = map[string]any{CodexTurnStateExtraKey: map[string]any{"collector_proxy_id": 8}}
	require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntent{}))
	require.Equal(t, []int64{8, 9}, CodexTurnStateCollectorProxyIDs(CodexTurnStateConfigForAccount(&stale)))
}

func TestCodexTurnStateMultiProxyGenerationUsesOrderAndEffectiveList(t *testing.T) {
	current := codexConfigAccount(t)
	current.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": true, "account_type": "auto", "collector_proxy_id": 8}
	before := CodexTurnStateGenerationForAccount(current)
	target := *current
	newFormat := CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyIDs: []int64{8}}
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &newFormat}))
	require.Equal(t, before, CodexTurnStateGenerationForAccount(&target))
	require.False(t, CodexTurnStateCollectorProxyOnlyChanged(current, &target))
	current.Extra = maps.Clone(current.Extra)
	current.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyIDs: []int64{8, 9}})
	newFormat.CollectorProxyIDs = []int64{9, 8}
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &newFormat}))
	require.NotEqual(t, before, CodexTurnStateGenerationForAccount(&target))
	require.True(t, CodexTurnStateCollectorProxyOnlyChanged(current, &target))
}
