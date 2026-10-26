//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestReferralRegistrationPendingOAuth_AtomicFinalize(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit_%t", commit), func(t *testing.T) {
			ctx := context.Background()
			auth, users, affiliates := newReferralRegistrationAuth(t, nil)
			inviter, code := newReferralInviter(t, users, affiliates)
			_, otherCode := newReferralInviter(t, users, affiliates)
			email := fmt.Sprintf("pending-referral-%d@example.com", time.Now().UnixNano())
			tx, err := integrationEntClient.Tx(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			txCtx := dbent.NewTxContext(ctx, tx)
			pair, user, err := auth.RegisterVerifiedOAuthEmailAccount(txCtx, email, "password123", code, "google", otherCode)
			require.NoError(t, err)
			require.NotNil(t, pair)
			require.NoError(t, auth.FinalizeOAuthEmailAccount(txCtx, user, code, "google", otherCode))
			if commit {
				require.NoError(t, tx.Commit())
				profile, err := affiliates.EnsureUserAffiliate(ctx, user.ID)
				require.NoError(t, err)
				require.NotNil(t, profile.InviterID)
				require.Equal(t, inviter.ID, *profile.InviterID)
			} else {
				require.NoError(t, tx.Rollback())
				_, err := users.GetByEmail(ctx, email)
				require.ErrorIs(t, err, service.ErrUserNotFound)
			}
			profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
			require.NoError(t, err)
			if commit {
				require.Equal(t, 1, profile.AffCount)
			} else {
				require.Zero(t, profile.AffCount)
			}
		})
	}
}

func TestReferralRegistrationPendingOAuth_BindingFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, func(repo service.AffiliateRepository) service.AffiliateRepository {
		return &failingRegistrationAffiliateRepository{AffiliateRepository: repo}
	})
	inviter, code := newReferralInviter(t, users, affiliates)
	email := fmt.Sprintf("pending-referral-bind-fail-%d@example.com", time.Now().UnixNano())
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	_, user, err := auth.RegisterVerifiedOAuthEmailAccount(txCtx, email, "password123", "", "github", code)
	require.NoError(t, err)
	require.Error(t, auth.FinalizeOAuthEmailAccount(txCtx, user, "", "github", code))
	require.NoError(t, tx.Rollback())
	_, err = users.GetByEmail(ctx, email)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	require.Zero(t, profile.AffCount)
}

func TestReferralRegistrationVerifiedEmailOAuth_AdmissionAndExistingLogin(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, nil)
	inviter, code := newReferralInviter(t, users, affiliates)
	for _, provider := range []string{"github", "google", "oidc"} {
		input := service.EmailOAuthIdentityInput{
			ProviderType: provider, ProviderSubject: fmt.Sprintf("referral-%s-%d", provider, time.Now().UnixNano()),
			Email: fmt.Sprintf("verified-%s-%d@example.com", provider, time.Now().UnixNano()), EmailVerified: true,
		}
		_, user, err := auth.LoginOrRegisterVerifiedEmailOAuthWithInvitation(ctx, input, "", code)
		require.NoError(t, err)
		profile, err := affiliates.EnsureUserAffiliate(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, profile.InviterID)
		require.Equal(t, inviter.ID, *profile.InviterID)
		_, existing, err := auth.LoginOrRegisterVerifiedEmailOAuthWithInvitation(ctx, input, "INVALID", "INVALID")
		require.NoError(t, err)
		require.Equal(t, user.ID, existing.ID)
	}
	profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, 3, profile.AffCount)
}

func TestReferralRegistrationVerifiedEmailOAuth_BindingFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, func(repo service.AffiliateRepository) service.AffiliateRepository {
		return &failingRegistrationAffiliateRepository{AffiliateRepository: repo}
	})
	inviter, code := newReferralInviter(t, users, affiliates)
	email := fmt.Sprintf("verified-referral-bind-fail-%d@example.com", time.Now().UnixNano())
	_, _, err := auth.LoginOrRegisterVerifiedEmailOAuthWithInvitation(ctx, service.EmailOAuthIdentityInput{
		ProviderType: "google", ProviderSubject: fmt.Sprintf("referral-fail-%d", time.Now().UnixNano()), Email: email, EmailVerified: true,
	}, code, "")
	require.Error(t, err)
	_, err = users.GetByEmail(ctx, email)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	require.Zero(t, profile.AffCount)
}

func TestReferralRegistrationPendingOAuth_QuotaBootstrapAfterCommitOnly(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit_%t", commit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, users, affiliates := newReferralRegistrationAuth(t, nil)
			_, code := newReferralInviter(t, users, affiliates)
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "pending-referral-quota-test", ExpireHour: 1, AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 7}}
			settings := service.NewSettingService(&referralRegistrationSettings{values: map[string]string{
				service.SettingKeyRegistrationEnabled: "true", service.SettingKeyInvitationCodeEnabled: "true", service.SettingKeyAffiliateEnabled: "true",
				service.SettingKeyDefaultPlatformQuotas: `{"openai":{"daily":5}}`,
			}}, cfg)
			quotas := NewUserPlatformQuotaRepository(integrationEntClient)
			auth := service.NewAuthService(integrationEntClient, users, NewRedeemCodeRepository(integrationEntClient), NewRefreshTokenCache(integrationRedis), cfg, settings, nil, nil, nil, nil, nil,
				service.NewAffiliateService(affiliates, settings, nil, nil), NewUserPlatformQuotaServiceAdapter(quotas))
			tx, err := integrationEntClient.Tx(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			txCtx := dbent.NewTxContext(ctx, tx)
			_, user, err := auth.RegisterVerifiedOAuthEmailAccount(txCtx, fmt.Sprintf("pending-referral-quota-%d@example.com", time.Now().UnixNano()), "password123", code, "google")
			require.NoError(t, err)
			require.NoError(t, auth.FinalizeOAuthEmailAccount(txCtx, user, code, "google", ""))
			if commit {
				require.NoError(t, tx.Commit())
				quota, err := quotas.GetByUserPlatform(ctx, user.ID, "openai")
				require.NoError(t, err)
				require.NotNil(t, quota, "quota snapshot must run after the new user commits")
				require.NotNil(t, quota.DailyLimitUSD)
				require.Equal(t, float64(5), *quota.DailyLimitUSD)
			} else {
				require.NoError(t, tx.Rollback())
				quota, err := quotas.GetByUserPlatform(ctx, user.ID, "openai")
				require.NoError(t, err)
				require.Nil(t, quota)
			}
		})
	}
}
