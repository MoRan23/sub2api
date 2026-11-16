package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptConfigReloadReusesCompiledRulesAndGate(t *testing.T) {
	settings := &staticSettingRepository{values: map[string]string{
		SettingKeyRiskControl: "true", SettingKeyPromptAuditConfig: `{"config_version":2,"keyword_blocking_enabled":true,"jailbreak_keywords":["regex:block-[0-9]+"],"other_keywords":["ordinary"]}`,
	}}
	m := NewConfigManager(nil, settings, nil, prefixEncryptor{}, testTotpKeyConfig())
	require.NoError(t, m.Reload(context.Background()))
	first := m.snapshot.Load()
	require.NotNil(t, first.active.jailbreakMatcher)
	require.NoError(t, m.Reload(context.Background()))
	require.Same(t, first.active.jailbreakMatcher, m.snapshot.Load().active.jailbreakMatcher)
	settings.values[SettingKeyRiskControl] = "false"
	require.NoError(t, m.Reload(context.Background()))
	require.Same(t, first.active.jailbreakMatcher, m.snapshot.Load().active.jailbreakMatcher)
	require.False(t, m.snapshot.Load().active.RiskControlEnabled)
	require.True(t, first.active.RiskControlEnabled, "borrowed snapshots remain immutable")
	settings.values[SettingKeyPromptAuditConfig] = `{"config_version":3,"keyword_blocking_enabled":true,"jailbreak_keywords":["changed"]}`
	require.NoError(t, m.Reload(context.Background()))
	require.NotSame(t, first.active.jailbreakMatcher, m.snapshot.Load().active.jailbreakMatcher)
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 100 {
				cfg, ok := readOnlyActiveConfig(m)
				if !ok {
					t.Error("missing snapshot")
					return
				}
				cfg.MatchJailbreakKeyword("changed")
			}
		})
	}
	for range 20 {
		require.NoError(t, m.Reload(context.Background()))
	}
	readers.Wait()
}

func BenchmarkPromptConfigUnchangedReload(b *testing.B) {
	cfg := DefaultStorageConfig()
	cfg.JailbreakKeywords = make([]string, 10000)
	for i := range cfg.JailbreakKeywords {
		cfg.JailbreakKeywords[i] = fmt.Sprintf("blocked-token-%05d", i)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		b.Fatal(err)
	}
	m := NewConfigManager(nil, staticSettingRepository{values: map[string]string{SettingKeyRiskControl: "true", SettingKeyPromptAuditConfig: string(raw)}}, nil, prefixEncryptor{}, testTotpKeyConfig())
	if err := m.Reload(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := m.Reload(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
