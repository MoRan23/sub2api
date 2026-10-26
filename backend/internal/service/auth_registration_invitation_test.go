//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type registrationAffiliateRepoStub struct {
	AffiliateRepository
	profiles map[string]*AffiliateSummary
}

func (r *registrationAffiliateRepoStub) GetAffiliateByCode(_ context.Context, code string) (*AffiliateSummary, error) {
	if p := r.profiles[code]; p != nil {
		return p, nil
	}
	return nil, ErrAffiliateProfileNotFound
}

func newRegistrationInvitationTestService() *AuthService {
	users := newRaceSafeUserRepo()
	users.byID[42] = &User{ID: 42, Status: StatusActive}
	users.byID[43] = &User{ID: 43, Status: "disabled"}
	svc := newOAuthEmailFlowAuthService(users, &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{
		"LEGACY": {ID: 1, Type: RedeemTypeInvitation, Status: StatusUnused},
		"USED":   {ID: 2, Type: RedeemTypeInvitation, Status: StatusUsed},
	}}, &refreshTokenCacheStub{}, map[string]string{
		SettingKeyRegistrationEnabled:   "true",
		SettingKeyInvitationCodeEnabled: "true",
		SettingKeyAffiliateEnabled:      "true",
	}, nil, &userPlatformQuotaRepoStub{})
	svc.affiliateService = NewAffiliateService(&registrationAffiliateRepoStub{profiles: map[string]*AffiliateSummary{
		"REFERRAL": {UserID: 42}, "USED": {UserID: 42}, "DISABLED": {UserID: 43}, "DELETED": {UserID: 44},
	}}, svc.settingService, nil, nil)
	return svc
}

func TestRegistrationInvitationResolution(t *testing.T) {
	for _, tc := range []struct {
		name, invitation, affiliate string
		wantErr                     error
		wantReferral                bool
	}{
		{name: "legacy", invitation: "LEGACY"},
		{name: "reusable referral", invitation: " referral ", wantReferral: true},
		{name: "referral link only", affiliate: "REFERRAL", wantReferral: true},
		{name: "explicit invalid beats referral", invitation: "INVALID", affiliate: "REFERRAL", wantErr: ErrInvitationCodeInvalid},
		{name: "used legacy stays single use", invitation: "USED", wantErr: ErrInvitationCodeInvalid},
		{name: "disabled inviter", invitation: "DISABLED", wantErr: ErrInvitationCodeInvalid},
		{name: "deleted inviter", invitation: "DELETED", wantErr: ErrInvitationCodeInvalid},
		{name: "missing", wantErr: ErrInvitationCodeRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newRegistrationInvitationTestService()
			admission, err := svc.resolveRegistrationInvitation(context.Background(), tc.invitation, tc.affiliate)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, admission)
			require.Equal(t, tc.wantReferral, admission.affiliateCode != "")
			if tc.wantReferral {
				require.Equal(t, int64(42), admission.inviterID)
				require.Empty(t, admission.optionalAffiliateCode("OTHER"))
			}
		})
	}
}

func TestRegistrationInvitationAffiliateDisabled(t *testing.T) {
	svc := newRegistrationInvitationTestService()
	svc.settingService.settingRepo.(*settingRepoStub).values[SettingKeyAffiliateEnabled] = "false"
	_, err := svc.resolveRegistrationInvitation(context.Background(), "REFERRAL", "")
	require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	_, err = svc.resolveRegistrationInvitation(context.Background(), "LEGACY", "")
	require.NoError(t, err)
}

func TestRegistrationInvitationPublicValidationAndMissingTransaction(t *testing.T) {
	svc := newRegistrationInvitationTestService()
	ctx := context.Background()
	require.Empty(t, svc.ValidateRegistrationInvitationCode(ctx, "referral"))
	require.Equal(t, "INVITATION_CODE_USED", svc.ValidateRegistrationInvitationCode(ctx, "USED"))
	admission, err := svc.resolveRegistrationInvitation(ctx, "REFERRAL", "")
	require.NoError(t, err)
	created := false
	err = svc.createUserWithInvitation(ctx, &User{}, admission, func(context.Context, *User) error {
		created = true
		return nil
	})
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.False(t, created, "mandatory inviter binding cannot create a user without a transaction")
}

func TestRegistrationInvitationExistingOAuthLoginDoesNotRequireAdmission(t *testing.T) {
	svc := newRegistrationInvitationTestService()
	users := svc.userRepo.(*raceSafeUserRepo)
	existing := &User{ID: 42, Email: "existing@example.com", Username: "Existing", Status: StatusActive}
	users.byID[42] = existing
	users.byEmail[existing.Email] = existing
	_, user, err := svc.LoginOrRegisterOAuthWithTokenPair(context.Background(), existing.Email, "Existing", "INVALID", "INVALID", "oidc")
	require.NoError(t, err)
	require.Equal(t, existing.ID, user.ID)
}
