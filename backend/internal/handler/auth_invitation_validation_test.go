package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type invitationValidationUsers struct{ service.UserRepository }

func (*invitationValidationUsers) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 42, Status: service.StatusActive}, nil
}

type invitationValidationRedeems struct{ service.RedeemCodeRepository }

func (*invitationValidationRedeems) GetByCode(_ context.Context, code string) (*service.RedeemCode, error) {
	if code == "USED" {
		return &service.RedeemCode{Type: service.RedeemTypeInvitation, Status: service.StatusUsed}, nil
	}
	return nil, service.ErrRedeemCodeNotFound
}

type invitationValidationAffiliates struct{ service.AffiliateRepository }

func (*invitationValidationAffiliates) GetAffiliateByCode(_ context.Context, code string) (*service.AffiliateSummary, error) {
	if code == "REFERRAL" {
		return &service.AffiliateSummary{UserID: 42, AffCode: code}, nil
	}
	return nil, service.ErrAffiliateProfileNotFound
}

type invitationValidationSettings struct {
	service.SettingRepository
	affiliateEnabled bool
}

func (r *invitationValidationSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyInvitationCodeEnabled || key == service.SettingKeyAffiliateEnabled && r.affiliateEnabled {
		return "true", nil
	}
	return "false", nil
}

func TestValidateInvitationCodeAcceptsReferralWithCompatibleResponse(t *testing.T) {
	for _, tc := range []struct {
		name, code, errorCode string
		affiliateEnabled      bool
		valid                 bool
	}{
		{name: "active referral", code: "referral", affiliateEnabled: true, valid: true},
		{name: "affiliate disabled", code: "REFERRAL", errorCode: "INVITATION_CODE_NOT_FOUND"},
		{name: "used legacy invitation", code: "USED", affiliateEnabled: true, errorCode: "INVITATION_CODE_USED"},
		{name: "unknown", code: "UNKNOWN", affiliateEnabled: true, errorCode: "INVITATION_CODE_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			settings := service.NewSettingService(&invitationValidationSettings{affiliateEnabled: tc.affiliateEnabled}, cfg)
			affiliates := service.NewAffiliateService(&invitationValidationAffiliates{}, settings, nil, nil)
			auth := service.NewAuthService(nil, &invitationValidationUsers{}, &invitationValidationRedeems{}, nil, cfg, settings, nil, nil, nil, nil, nil, affiliates, nil)
			handler := &AuthHandler{authService: auth, settingSvc: settings}
			body, err := json.Marshal(ValidateInvitationCodeRequest{Code: tc.code})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/validate-invitation-code", strings.NewReader(string(body)))
			c.Request.Header.Set("Content-Type", "application/json")
			handler.ValidateInvitationCode(c)
			require.Equal(t, http.StatusOK, w.Code)
			var envelope struct {
				Data ValidateInvitationCodeResponse `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
			require.Equal(t, tc.valid, envelope.Data.Valid)
			require.Equal(t, tc.errorCode, envelope.Data.ErrorCode)
		})
	}
}
