//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDaybreakSchedulerCacheRoundTrip(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	a := &service.Account{ID: 778, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true, service.OpenAIDaybreakRedEnabledKey: false}}
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err := cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.Equal(t, true, got.Extra[service.OpenAIDaybreakBlueEnabledKey])
	require.Equal(t, false, got.Extra[service.OpenAIDaybreakRedEnabledKey])
	patch := accountConfigurationExtraPatch(context.Background(), []int64{a.ID}, a.Extra)
	require.NotContains(t, patch, service.OpenAIDaybreakBlueEnabledKey)
	require.NotContains(t, patch, service.OpenAIDaybreakRedEnabledKey)
}
