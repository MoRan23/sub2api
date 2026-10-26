//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type referralRegistrationSettings struct {
	service.SettingRepository
	values map[string]string
}

func (r *referralRegistrationSettings) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *referralRegistrationSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func newReferralRegistrationAuth(t *testing.T, decorate func(service.AffiliateRepository) service.AffiliateRepository) (*service.AuthService, service.UserRepository, service.AffiliateRepository) {
	t.Helper()
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-referral-registration-secret", ExpireHour: 1, AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 7}}
	settings := service.NewSettingService(&referralRegistrationSettings{values: map[string]string{
		service.SettingKeyRegistrationEnabled: "true", service.SettingKeyInvitationCodeEnabled: "true", service.SettingKeyAffiliateEnabled: "true",
	}}, cfg)
	users := NewUserRepository(integrationEntClient, integrationDB)
	affiliates := NewAffiliateRepository(integrationEntClient, integrationDB)
	if decorate != nil {
		affiliates = decorate(affiliates)
	}
	auth := service.NewAuthService(integrationEntClient, users, NewRedeemCodeRepository(integrationEntClient), NewRefreshTokenCache(integrationRedis), cfg, settings, nil, nil, nil, nil, nil, service.NewAffiliateService(affiliates, settings, nil, nil), nil)
	return auth, users, affiliates
}

func newReferralInviter(t *testing.T, users service.UserRepository, affiliates service.AffiliateRepository) (*service.User, string) {
	t.Helper()
	u := &service.User{Email: fmt.Sprintf("referral-owner-%d@example.com", time.Now().UnixNano()), PasswordHash: "hash", Role: service.RoleUser, Status: service.StatusActive}
	require.NoError(t, users.Create(context.Background(), u))
	profile, err := affiliates.EnsureUserAffiliate(context.Background(), u.ID)
	require.NoError(t, err)
	return u, profile.AffCode
}

func TestReferralRegistration_ReusableAdmissionAndOAuth(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, nil)
	inviter, code := newReferralInviter(t, users, affiliates)
	_, otherCode := newReferralInviter(t, users, affiliates)
	require.Empty(t, auth.ValidateRegistrationInvitationCode(ctx, code))
	for i := 0; i < 2; i++ {
		email := fmt.Sprintf("referral-signup-%d-%d@example.com", time.Now().UnixNano(), i)
		_, user, err := auth.RegisterWithVerification(ctx, email, "password123", "", "", code, otherCode)
		require.NoError(t, err)
		profile, err := affiliates.EnsureUserAffiliate(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, profile.InviterID)
		require.Equal(t, inviter.ID, *profile.InviterID, "admission referral wins over an unrelated aff_code")
	}
	for _, source := range []string{"linuxdo", "oidc", "wechat", "dingtalk"} {
		email := fmt.Sprintf("referral-%s-%d@example.com", source, time.Now().UnixNano())
		_, user, err := auth.LoginOrRegisterOAuthWithTokenPair(ctx, email, "OAuth user", "", code, source)
		require.NoError(t, err)
		profile, err := affiliates.EnsureUserAffiliate(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, profile.InviterID)
		require.Equal(t, inviter.ID, *profile.InviterID)
	}
	profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, 6, profile.AffCount)
	require.Empty(t, auth.ValidateRegistrationInvitationCode(ctx, code), "referrals remain reusable")
}

type failingRegistrationAffiliateRepository struct {
	service.AffiliateRepository
}

func (r *failingRegistrationAffiliateRepository) BindRegistrationInviter(ctx context.Context, userID, inviterID int64, code string) error {
	if dbent.TxFromContext(ctx) == nil {
		return errors.New("registration transaction missing")
	}
	if err := r.AffiliateRepository.(service.AffiliateRegistrationRepository).BindRegistrationInviter(ctx, userID, inviterID, code); err != nil {
		return err
	}
	return errors.New("simulated failure after binding")
}

func TestReferralRegistration_BindingFailureRollsBackUserAndInviterCount(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, func(repo service.AffiliateRepository) service.AffiliateRepository {
		return &failingRegistrationAffiliateRepository{AffiliateRepository: repo}
	})
	inviter, code := newReferralInviter(t, users, affiliates)
	for _, oauth := range []bool{false, true} {
		email := fmt.Sprintf("referral-rollback-%t-%d@example.com", oauth, time.Now().UnixNano())
		var err error
		if oauth {
			_, _, err = auth.LoginOrRegisterOAuthWithTokenPair(ctx, email, "OAuth", code, "", "oidc")
		} else {
			_, _, err = auth.RegisterWithVerification(ctx, email, "password123", "", "", code, "")
		}
		require.Error(t, err)
		_, err = users.GetByEmail(ctx, email)
		require.ErrorIs(t, err, service.ErrUserNotFound)
	}
	profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	require.Zero(t, profile.AffCount)
}

func TestReferralRegistration_LegacyInvitationConcurrentSingleUse(t *testing.T) {
	ctx := context.Background()
	auth, _, _ := newReferralRegistrationAuth(t, nil)
	code := &service.RedeemCode{Code: fmt.Sprintf("INV-%d", time.Now().UnixNano()), Type: service.RedeemTypeInvitation, Status: service.StatusUnused}
	require.NoError(t, NewRedeemCodeRepository(integrationEntClient).Create(ctx, code))
	prefix := fmt.Sprintf("legacy-concurrent-%d-", time.Now().UnixNano())
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := auth.RegisterWithVerification(ctx, fmt.Sprintf("%s%d@example.com", prefix, i), "password123", "", "", code.Code, "")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, querySingleInt(t, ctx, integrationEntClient, "SELECT COUNT(*) FROM users WHERE email LIKE $1", prefix+"%"))
	require.Equal(t, "INVITATION_CODE_USED", auth.ValidateRegistrationInvitationCode(ctx, code.Code))
}

func TestReferralRegistration_InviterMustRemainActive(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, nil)
	inviter, code := newReferralInviter(t, users, affiliates)
	require.Empty(t, auth.ValidateRegistrationInvitationCode(ctx, code))
	_, err := integrationEntClient.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = $1", inviter.ID)
	require.NoError(t, err)
	require.NotEmpty(t, auth.ValidateRegistrationInvitationCode(ctx, code))
	_, err = integrationEntClient.ExecContext(ctx, "UPDATE users SET status = 'active', deleted_at = NOW() WHERE id = $1", inviter.ID)
	require.NoError(t, err)
	require.NotEmpty(t, auth.ValidateRegistrationInvitationCode(ctx, code))
}

func TestReferralRegistration_ConcurrentReusableCode(t *testing.T) {
	ctx := context.Background()
	auth, users, affiliates := newReferralRegistrationAuth(t, nil)
	inviter, code := newReferralInviter(t, users, affiliates)
	prefix := fmt.Sprintf("referral-concurrent-%d-", time.Now().UnixNano())
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := auth.RegisterWithVerification(ctx, fmt.Sprintf("%s%d@example.com", prefix, i), "password123", "", "", code, "")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	profile, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, 3, profile.AffCount)
}
