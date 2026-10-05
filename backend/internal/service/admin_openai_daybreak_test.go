package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type daybreakAdminReader func(context.Context, *Account) (*OpenAIDaybreakCapabilities, error)

func (f daybreakAdminReader) GetOpenAIDaybreakCapabilities(ctx context.Context, a *Account) (*OpenAIDaybreakCapabilities, error) {
	return f(ctx, a)
}

func TestDaybreakAdminValidation(t *testing.T) {
	account := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	for _, value := range []any{"true", 1, nil} {
		_, err := NormalizeOpenAIDaybreakSettings(account, map[string]any{OpenAIDaybreakBlueEnabledKey: value})
		require.Error(t, err)
	}
	_, err := NormalizeOpenAIDaybreakSettings(account, map[string]any{OpenAIDaybreakRedEnabledKey: true})
	require.Error(t, err)
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeSetupToken} {
		other := *account
		other.Type = kind
		_, err := NormalizeOpenAIDaybreakSettings(&other, map[string]any{OpenAIDaybreakBlueEnabledKey: true})
		require.Error(t, err)
	}
	account.Extra[OpenAIDaybreakBlueEnabledKey] = true
	account.Extra[OpenAIDaybreakRedEnabledKey] = true
	patch, err := NormalizeOpenAIDaybreakSettings(account, map[string]any{OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: true})
	require.NoError(t, err)
	require.Equal(t, false, patch[OpenAIDaybreakRedEnabledKey])
	account.Extra = patch
	require.NoError(t, validateNewAccountDaybreak(account))
	account.Extra[OpenAIDaybreakBlueEnabledKey] = true
	require.Error(t, validateNewAccountDaybreak(account))
}

func TestDaybreakAdminCapabilityIOAndEvidence(t *testing.T) {
	ctx := context.Background()
	account := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: true}}
	calls := 0
	svc := &adminServiceImpl{daybreakCapabilities: daybreakAdminReader(func(context.Context, *Account) (*OpenAIDaybreakCapabilities, error) {
		calls++
		return &OpenAIDaybreakCapabilities{BlueAvailable: true, RedAvailable: true, CredentialOwnerID: 15, CredentialOS: "linux", AuthorizationGeneration: "grant-one"}, nil
	})}
	for _, patch := range []map[string]any{nil, {"unrelated": true}, {OpenAIDaybreakBlueEnabledKey: true}, {OpenAIDaybreakBlueEnabledKey: false}} {
		_, _, err := svc.prepareDaybreakUpdate(ctx, account, patch)
		require.NoError(t, err)
	}
	require.Zero(t, calls)
	updatedCtx, _, err := svc.prepareDaybreakUpdate(ctx, account, map[string]any{OpenAIDaybreakRedEnabledKey: true})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	evidence, ok := AccountDaybreakEvidenceFromContext(updatedCtx, 15)
	require.True(t, ok)
	require.Equal(t, "grant-one", evidence.AuthorizationGeneration)
	_, ok = AccountDaybreakEvidenceFromContext(updatedCtx, 16)
	require.False(t, ok)
	svc.daybreakCapabilities = daybreakAdminReader(func(context.Context, *Account) (*OpenAIDaybreakCapabilities, error) {
		return nil, errors.New("unavailable")
	})
	_, _, err = svc.prepareDaybreakUpdate(ctx, account, map[string]any{OpenAIDaybreakRedEnabledKey: true})
	require.Error(t, err)
}

func TestDaybreakConfigurationPreservesOmittedAndBackgroundSnapshots(t *testing.T) {
	current := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}}
	target := *current
	target.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: false}
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
	require.Equal(t, true, target.Extra[OpenAIDaybreakBlueEnabledKey])
	require.Equal(t, true, target.Extra[OpenAIDaybreakRedEnabledKey])
	ctx := withAccountConfigurationIntent(context.Background(), []int64{15}, map[string]any{OpenAIDaybreakBlueEnabledKey: false}, nil)
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntentFromContext(ctx, 15)))
	require.Equal(t, false, target.Extra[OpenAIDaybreakBlueEnabledKey])
	require.Equal(t, false, target.Extra[OpenAIDaybreakRedEnabledKey])
	shadowID := int64(12)
	target.ParentAccountID = &shadowID
	target.Extra = nil
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
	require.Equal(t, true, target.Extra[OpenAIDaybreakBlueEnabledKey], "business account keeps independent settings")
}
