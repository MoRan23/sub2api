package service

import (
	"context"
	"errors"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// registrationInvitation distinguishes reusable referrals from single-use
// registration invitations. A referral used for admission must be bound before
// the transaction creating the user can commit.
type registrationInvitation struct {
	redeem        *RedeemCode
	affiliateCode string
	inviterID     int64
}

// AffiliateRegistrationRepository validates and locks the inviter and referral
// code while binding, using the caller's registration transaction.
type AffiliateRegistrationRepository interface {
	BindRegistrationInviter(ctx context.Context, userID, inviterID int64, code string) error
}

func (s *AuthService) resolveRegistrationInvitation(ctx context.Context, invitationCode, affiliateCode string) (*registrationInvitation, error) {
	if s == nil || s.settingService == nil || !s.settingService.IsInvitationCodeEnabled(ctx) {
		return nil, nil
	}
	code := strings.TrimSpace(invitationCode)
	if code == "" {
		code = strings.TrimSpace(affiliateCode)
	}
	if code == "" {
		return nil, ErrInvitationCodeRequired
	}
	invitation, errorCode := s.validateRegistrationInvitationCode(ctx, code)
	if errorCode != "" {
		return nil, ErrInvitationCodeInvalid
	}
	return invitation, nil
}

// ValidateRegistrationInvitationCode retains the public validator's error-code
// contract while recognizing active inviters' reusable affiliate codes.
func (s *AuthService) ValidateRegistrationInvitationCode(ctx context.Context, code string) string {
	_, errorCode := s.validateRegistrationInvitationCode(ctx, strings.TrimSpace(code))
	return errorCode
}

func (s *AuthService) validateRegistrationInvitationCode(ctx context.Context, code string) (*registrationInvitation, string) {
	if s == nil {
		return nil, "INVITATION_CODE_INVALID"
	}
	if s.redeemRepo != nil || s.oauthEmailFlowClient(ctx) != nil {
		redeem, err := s.loadOAuthRegistrationInvitation(ctx, code)
		if err == nil && redeem != nil {
			// A known legacy code keeps its original semantics, even if another
			// namespace happens to contain the same text.
			if redeem.Type != RedeemTypeInvitation {
				return nil, "INVITATION_CODE_INVALID"
			}
			if !redeem.CanUse() {
				return nil, "INVITATION_CODE_USED"
			}
			return &registrationInvitation{redeem: redeem}, ""
		}
		if err != nil && !errors.Is(err, ErrRedeemCodeNotFound) && !dbent.IsNotFound(err) {
			return nil, "INVITATION_CODE_INVALID"
		}
	}
	if s.affiliateService == nil || s.affiliateService.repo == nil || !s.affiliateService.IsEnabled(ctx) || s.userRepo == nil {
		return nil, "INVITATION_CODE_NOT_FOUND"
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if !isValidAffiliateCodeFormat(code) {
		return nil, "INVITATION_CODE_INVALID"
	}
	inviter, err := s.affiliateService.repo.GetAffiliateByCode(ctx, code)
	if err != nil || inviter == nil || inviter.UserID <= 0 {
		return nil, "INVITATION_CODE_NOT_FOUND"
	}
	user, err := s.userRepo.GetByID(ctx, inviter.UserID)
	if err != nil || user == nil || !user.IsActive() {
		return nil, "INVITATION_CODE_INVALID"
	}
	return &registrationInvitation{affiliateCode: code, inviterID: inviter.UserID}, ""
}

func (s *AuthService) consumeRegistrationInvitation(ctx context.Context, userID int64, invitation *registrationInvitation) error {
	if invitation == nil {
		return nil
	}
	if invitation.redeem != nil {
		if err := s.useOAuthRegistrationInvitation(ctx, invitation.redeem.ID, userID); err != nil {
			return ErrInvitationCodeInvalid
		}
		return nil
	}
	if s.affiliateService == nil || !s.affiliateService.IsEnabled(ctx) || userID == invitation.inviterID {
		return ErrInvitationCodeInvalid
	}
	repo, ok := s.affiliateService.repo.(AffiliateRegistrationRepository)
	if !ok || dbent.TxFromContext(ctx) == nil {
		return ErrServiceUnavailable
	}
	if err := repo.BindRegistrationInviter(ctx, userID, invitation.inviterID, invitation.affiliateCode); err != nil {
		return err
	}
	return nil
}

func (s *AuthService) createUserWithInvitation(ctx context.Context, user *User, invitation *registrationInvitation, create func(context.Context, *User) error) error {
	commitUser := func(txCtx context.Context) error {
		if err := create(txCtx, user); err != nil {
			return err
		}
		return s.consumeRegistrationInvitation(txCtx, user.ID, invitation)
	}
	if invitation == nil || dbent.TxFromContext(ctx) != nil {
		return commitUser(ctx)
	}
	if s.entClient == nil {
		// Referral admission must never create an unbound user. The existing
		// single-use test adapters retain their conditional-claim fallback.
		if invitation.affiliateCode != "" {
			return ErrServiceUnavailable
		}
		return commitUser(ctx)
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if err := commitUser(dbent.NewTxContext(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrServiceUnavailable
	}
	return nil
}

func (i *registrationInvitation) optionalAffiliateCode(code string) string {
	if i != nil && i.affiliateCode != "" {
		// Already bound atomically; an unrelated aff_code must not change it.
		return ""
	}
	return code
}
