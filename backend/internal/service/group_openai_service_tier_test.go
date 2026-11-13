package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGroupOpenAIServiceTier(t *testing.T) {
	yes, no := true, false
	for _, platform := range []string{PlatformOpenAI, PlatformComposite} {
		t.Run(platform, func(t *testing.T) {
			fast, ultra, freeFast, freeUltra, err := ResolveGroupOpenAIServiceTier(platform, false, true, true, true, nil)
			require.NoError(t, err)
			require.False(t, fast)
			require.True(t, ultra)
			require.True(t, freeFast)
			require.True(t, freeUltra)

			_, _, _, _, err = ResolveGroupOpenAIServiceTier(platform, false, true, true, true, &GroupOpenAIServiceTierUpdate{ForceFast: &yes})
			require.ErrorContains(t, err, "cannot both be enabled")

			fast, ultra, freeFast, freeUltra, err = ResolveGroupOpenAIServiceTier(platform, false, true, true, true, &GroupOpenAIServiceTierUpdate{ForceFast: &yes, ForceUltrafast: &no})
			require.NoError(t, err)
			require.True(t, fast)
			require.False(t, ultra)
			require.True(t, freeFast, "omitted free preference must be retained")
			require.True(t, freeUltra)
		})
	}
	fast, ultra, freeFast, freeUltra, err := ResolveGroupOpenAIServiceTier(PlatformAnthropic, true, true, true, true, nil)
	require.NoError(t, err)
	require.False(t, fast)
	require.False(t, ultra)
	require.False(t, freeFast)
	require.False(t, freeUltra)
}
