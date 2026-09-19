package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProvidePluginManagerBindsHostServicesBeforeRuntimeStart(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	store := newFakePluginKVStore()
	manager := ProvidePluginManager(nil, nil, &config.Config{}, PluginHostInfo{Version: "0.2.7"}, store, gateway)
	require.Same(t, store, manager.kvStore)
	require.Same(t, gateway, manager.accountDirectory)
	installation := &PluginInstallation{PluginKey: "test.provider", Manifest: PluginManifest{Capabilities: []PluginCapability{
		{ID: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth},
	}}}
	endpoint, ok := manager.buildHostServices(installation).(*pluginHostServiceServer)
	require.True(t, ok)
	require.Same(t, store, endpoint.store)
	require.Same(t, gateway, endpoint.directory)
	require.Equal(t, "test.provider", endpoint.pluginKey)
	require.Empty(t, manager.runtimes, "provider construction must not start plugin processes")
}
