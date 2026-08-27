package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type groupApplicationServiceRegressionRepo struct {
	GroupApplicationRepository
	application       *GroupApplication
	getErr            error
	rejectErr         error
	revokeErr         error
	completeErr       error
	rejectMismatchErr error
	rejectCalls       int
	revokeCalls       int
	rejectReason      string
	revokeReason      string
	rejectMail        GroupApplicationMailJob
	revokeMail        GroupApplicationMailJob
	completeReceipt   *GroupApplicationReceipt
	mismatchReceipt   *GroupApplicationReceipt
}

type groupApplicationServiceRegressionSettings struct {
	values map[string]string
}

func (r *groupApplicationServiceRegressionSettings) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *groupApplicationServiceRegressionSettings) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *groupApplicationServiceRegressionSettings) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (*groupApplicationServiceRegressionSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("unused")
}

func (*groupApplicationServiceRegressionSettings) SetMultiple(context.Context, map[string]string) error {
	return errors.New("unused")
}

func (*groupApplicationServiceRegressionSettings) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("unused")
}

func (*groupApplicationServiceRegressionSettings) Delete(context.Context, string) error {
	return errors.New("unused")
}

type groupApplicationServiceRegressionEncryptor struct{}

func (groupApplicationServiceRegressionEncryptor) Encrypt(value string) (string, error) {
	return value, nil
}

func (groupApplicationServiceRegressionEncryptor) Decrypt(value string) (string, error) {
	return value, nil
}

func newGroupApplicationServiceRegressionSettings(t *testing.T, enabled bool) *groupApplicationServiceRegressionSettings {
	t.Helper()
	repo := &groupApplicationServiceRegressionSettings{values: map[string]string{}}
	if !enabled {
		return repo
	}
	svc := NewGroupApplicationService(nil, repo, groupApplicationServiceRegressionEncryptor{})
	_, err := svc.SaveEmailConfig(context.Background(), GroupApplicationEmailConfig{
		Enabled: true,
		SMTP: GroupApplicationSMTPConfig{
			Host: "smtp.example.com", Port: 587, Username: "sender@example.com", Password: "smtp-secret",
			FromAddress: "sender@example.com", FromName: "Applications", TLSMode: "starttls",
		},
		IMAP: GroupApplicationIMAPConfig{
			Host: "imap.example.com", Port: 993, Username: "inbox@example.com", Password: "imap-secret",
			Mailbox: "INBOX", ReplyAddress: "reply@example.com", TLSMode: "implicit", PollIntervalSeconds: 30,
		},
	})
	require.NoError(t, err)
	return repo
}

func (r *groupApplicationServiceRegressionRepo) GetApplication(context.Context, int64) (*GroupApplication, error) {
	return r.application, r.getErr
}

func (r *groupApplicationServiceRegressionRepo) Reject(_ context.Context, _ int64, _ int64, reason string, mail GroupApplicationMailJob) (*GroupApplication, error) {
	r.rejectCalls++
	r.rejectReason = reason
	r.rejectMail = mail
	return r.application, r.rejectErr
}

func (r *groupApplicationServiceRegressionRepo) Revoke(_ context.Context, _ int64, _ int64, reason string, mail GroupApplicationMailJob) (*GroupApplication, error) {
	r.revokeCalls++
	r.revokeReason = reason
	r.revokeMail = mail
	return r.application, r.revokeErr
}

func (r *groupApplicationServiceRegressionRepo) CompleteFromReply(_ context.Context, _ int64, _ GroupApplicationMailJob, receipt *GroupApplicationReceipt) (*GroupApplication, error) {
	r.completeReceipt = receipt
	return r.application, r.completeErr
}

func (r *groupApplicationServiceRegressionRepo) RejectReplyMismatch(_ context.Context, _ int64, _ GroupApplicationMailJob, receipt *GroupApplicationReceipt) (*GroupApplication, error) {
	r.mismatchReceipt = receipt
	return r.application, r.rejectMismatchErr
}

func newGroupApplicationServiceRegressionApplication() *GroupApplication {
	return &GroupApplication{
		ID:                  41,
		GroupName:           "Private",
		ContactEmail:        "applicant@example.com",
		Locale:              "en",
		Status:              GroupApplicationStatusAwaitingReply,
		ReplyPhraseSnapshot: "CONFIRM",
		TemplatesSnapshot:   DefaultGroupApplicationTemplates(),
		CreatedAt:           time.Now(),
	}
}

func TestGroupApplicationTerminalAdminActionsPersistWhenWorkflowDisabled(t *testing.T) {
	settings := newGroupApplicationServiceRegressionSettings(t, false)

	t.Run("reject", func(t *testing.T) {
		repo := &groupApplicationServiceRegressionRepo{application: newGroupApplicationServiceRegressionApplication()}
		svc := NewGroupApplicationService(repo, settings, groupApplicationServiceRegressionEncryptor{})

		application, err := svc.Reject(context.Background(), 41, 7, "  no longer eligible  ")
		require.NoError(t, err)
		require.Same(t, repo.application, application)
		require.Equal(t, 1, repo.rejectCalls)
		require.Equal(t, "no longer eligible", repo.rejectReason)
		require.Equal(t, GroupApplicationMailManualRejection, repo.rejectMail.Kind)
		require.Equal(t, GroupApplicationStatusRejected, repo.rejectMail.RequiredStatus)
	})

	t.Run("revoke", func(t *testing.T) {
		repo := &groupApplicationServiceRegressionRepo{application: newGroupApplicationServiceRegressionApplication()}
		svc := NewGroupApplicationService(repo, settings, groupApplicationServiceRegressionEncryptor{})

		application, err := svc.Revoke(context.Background(), 41, 7, "  policy changed  ")
		require.NoError(t, err)
		require.Same(t, repo.application, application)
		require.Equal(t, 1, repo.revokeCalls)
		require.Equal(t, "policy changed", repo.revokeReason)
		require.Equal(t, GroupApplicationMailRevocation, repo.revokeMail.Kind)
		require.Equal(t, GroupApplicationStatusRevoked, repo.revokeMail.RequiredStatus)
	})
}

func TestProcessInboundReplyClassifiesPermanentBusinessErrors(t *testing.T) {
	settings := newGroupApplicationServiceRegressionSettings(t, true)
	tests := []struct {
		name        string
		getErr      error
		completeErr error
		wantResult  string
		wantErr     error
		permanent   bool
	}{
		{name: "application disappeared", getErr: ErrGroupApplicationNotFound, wantResult: "not_found", wantErr: ErrGroupApplicationNotFound, permanent: true},
		{name: "application already terminal", completeErr: ErrGroupApplicationState, wantResult: "state_conflict", wantErr: ErrGroupApplicationState, permanent: true},
		{name: "group no longer eligible", completeErr: ErrGroupApplicationUnavailable, wantResult: "unavailable", wantErr: ErrGroupApplicationUnavailable, permanent: true},
		{name: "mail delivery barrier", completeErr: ErrGroupApplicationMailDeliveryInProgress, wantResult: "error", wantErr: ErrGroupApplicationMailDeliveryInProgress},
		{name: "database failure", completeErr: errors.New("database unavailable"), wantResult: "error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &groupApplicationServiceRegressionRepo{
				application: newGroupApplicationServiceRegressionApplication(),
				getErr:      test.getErr, completeErr: test.completeErr,
			}
			svc := NewGroupApplicationService(repo, settings, groupApplicationServiceRegressionEncryptor{})

			result, err := svc.ProcessInboundReply(context.Background(), 41, "applicant@example.com", "CONFIRM", nil)
			require.Error(t, err)
			require.Equal(t, test.wantResult, result)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			}
			require.Equal(t, test.permanent, isPermanentGroupApplicationInboundReplyError(err))
		})
	}
}

func TestProcessInboundReplyPassesTerminalReceiptToRepository(t *testing.T) {
	settings := newGroupApplicationServiceRegressionSettings(t, true)
	tests := []struct {
		name       string
		reply      string
		wantResult string
		captured   func(*groupApplicationServiceRegressionRepo) *GroupApplicationReceipt
	}{
		{
			name: "completed", reply: "CONFIRM", wantResult: "completed",
			captured: func(repo *groupApplicationServiceRegressionRepo) *GroupApplicationReceipt {
				return repo.completeReceipt
			},
		},
		{
			name: "reply mismatch", reply: "WRONG", wantResult: "reply_mismatch",
			captured: func(repo *groupApplicationServiceRegressionRepo) *GroupApplicationReceipt {
				return repo.mismatchReceipt
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &groupApplicationServiceRegressionRepo{application: newGroupApplicationServiceRegressionApplication()}
			service := NewGroupApplicationService(repo, settings, groupApplicationServiceRegressionEncryptor{})
			receipt := &GroupApplicationReceipt{MailboxFingerprint: "mailbox", UIDValidity: 12, UID: 34}

			result, err := service.ProcessInboundReply(
				context.Background(), repo.application.ID, repo.application.ContactEmail, test.reply, receipt,
			)

			require.NoError(t, err)
			require.Equal(t, test.wantResult, result)
			require.Same(t, receipt, test.captured(repo))
			require.Equal(t, test.wantResult, receipt.Result)
			require.NotNil(t, receipt.ApplicationID)
			require.Equal(t, repo.application.ID, *receipt.ApplicationID)
		})
	}
}

func TestGroupApplicationMailDeliveryBarrierHasStableConflictMapping(t *testing.T) {
	require.Equal(t, "GROUP_APPLICATION_MAIL_DELIVERY_IN_PROGRESS", infraerrors.Reason(ErrGroupApplicationMailDeliveryInProgress))
	require.Equal(t, 409, infraerrors.Code(ErrGroupApplicationMailDeliveryInProgress))
}

func TestGroupApplicationRevokePreservesAccessNotOwnedConflict(t *testing.T) {
	repo := &groupApplicationServiceRegressionRepo{
		application: newGroupApplicationServiceRegressionApplication(),
		revokeErr:   ErrGroupApplicationAccessNotOwned,
	}
	svc := NewGroupApplicationService(repo, newGroupApplicationServiceRegressionSettings(t, false), groupApplicationServiceRegressionEncryptor{})

	application, err := svc.Revoke(context.Background(), 41, 7, "access belongs to another grant")
	require.Same(t, repo.application, application)
	require.ErrorIs(t, err, ErrGroupApplicationAccessNotOwned)
	require.Equal(t, "GROUP_APPLICATION_ACCESS_NOT_OWNED", infraerrors.Reason(err))
	require.Equal(t, 409, infraerrors.Code(err))
	require.Equal(t, 1, repo.revokeCalls)
}

func TestDefaultGroupApplicationTemplatesAllowAutomaticEmailSignatures(t *testing.T) {
	templates := DefaultGroupApplicationTemplates()
	require.Contains(t, templates[GroupApplicationMailApproval]["zh"].HTML, "可保留邮箱自动签名")
	require.Contains(t, templates[GroupApplicationMailApproval]["en"].HTML, "automatic email signatures are allowed")
	require.Contains(t, templates[GroupApplicationMailReplyMismatch]["zh"].HTML, "回复首段")
	require.Contains(t, templates[GroupApplicationMailReplyMismatch]["en"].HTML, "first paragraph")
	require.NotContains(t, templates[GroupApplicationMailApproval]["zh"].HTML, "不得附加签名")
	require.NotContains(t, templates[GroupApplicationMailApproval]["en"].HTML, "with no signature")
}

func TestNormalizeGroupApplicationTemplatesUpgradesPreviousFancyDefaults(t *testing.T) {
	previous := previousDefaultGroupApplicationTemplates()
	require.Contains(t, previous[GroupApplicationMailApproval]["zh"].HTML, "不得附加签名")
	require.Contains(t, previous[GroupApplicationMailReplyMismatch]["en"].HTML, "Common causes include signatures")

	normalized, err := NormalizeGroupApplicationTemplates(previous)
	require.NoError(t, err)
	require.Equal(t, DefaultGroupApplicationTemplates(), normalized)

	custom := previousDefaultGroupApplicationTemplates()
	value := custom[GroupApplicationMailApproval]["zh"]
	value.HTML += "<p>custom policy text</p>"
	custom[GroupApplicationMailApproval]["zh"] = value
	normalized, err = NormalizeGroupApplicationTemplates(custom)
	require.NoError(t, err)
	require.Contains(t, normalized[GroupApplicationMailApproval]["zh"].HTML, "custom policy text")
}

func TestGroupApplicationBuildMailUpgradesPreviousDefaultSnapshot(t *testing.T) {
	application := newGroupApplicationServiceRegressionApplication()
	application.TemplatesSnapshot = previousDefaultGroupApplicationTemplates()
	service := NewGroupApplicationService(nil, nil, nil)

	mail, err := service.buildMail(context.Background(), application, GroupApplicationMailApproval, "")

	require.NoError(t, err)
	require.Contains(t, mail.HTMLBody, "automatic email signatures are allowed")
	require.NotContains(t, mail.HTMLBody, "with no signature")
}

func TestGroupApplicationSavePolicyRejectsMultilineReplyPhrase(t *testing.T) {
	service := NewGroupApplicationService(&groupApplicationServiceRegressionRepo{}, nil, nil)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			policy := &GroupApplicationPolicy{
				GroupID:     7,
				Enabled:     enabled,
				ReplyPhrase: "CONFIRM\r\nSECOND",
				Templates:   DefaultGroupApplicationTemplates(),
			}

			saved, err := service.SavePolicy(context.Background(), policy, nil, 1)

			require.Nil(t, saved)
			require.Error(t, err)
			require.Equal(t, 400, infraerrors.Code(err))
			require.Equal(t, "INVALID_GROUP_APPLICATION_REPLY_PHRASE", infraerrors.Reason(err))
			require.Contains(t, infraerrors.Message(err), "single line")
		})
	}
}
