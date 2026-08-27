//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGroupApplicationTemplatesRequiresWorkflowPlaceholders(t *testing.T) {
	templates, err := NormalizeGroupApplicationTemplates(nil)
	require.NoError(t, err)
	require.Contains(t, templates[GroupApplicationMailApproval]["zh"].HTML, "{{reply_phrase}}")
	require.Contains(t, templates[GroupApplicationMailRevocation]["en"].HTML, "{{decision_reason}}")

	templates[GroupApplicationMailApproval]["zh"] = GroupApplicationLocalizedTemplate{Subject: "approved", HTML: "<p>no phrase</p>"}
	_, err = NormalizeGroupApplicationTemplates(templates)
	require.ErrorContains(t, err, "{{reply_phrase}}")

	templates = DefaultGroupApplicationTemplates()
	templates[GroupApplicationMailCompletion]["en"] = GroupApplicationLocalizedTemplate{Subject: "{{unknown}}", HTML: "ok"}
	_, err = NormalizeGroupApplicationTemplates(templates)
	require.ErrorContains(t, err, "unknown placeholder")

	templates = DefaultGroupApplicationTemplates()
	templates[GroupApplicationMailApproval]["en"] = GroupApplicationLocalizedTemplate{Subject: "Approved", HTML: "Reply {{ reply_phrase }}"}
	_, err = NormalizeGroupApplicationTemplates(templates)
	require.NoError(t, err)

	templates[GroupApplicationMailCompletion]["en"] = GroupApplicationLocalizedTemplate{Subject: "{{INVALID-TOKEN}}", HTML: "ok"}
	_, err = NormalizeGroupApplicationTemplates(templates)
	require.ErrorContains(t, err, "invalid placeholder")
}

func TestRenderGroupApplicationTemplateEscapesVariables(t *testing.T) {
	subject, body, err := RenderGroupApplicationTemplate(
		GroupApplicationLocalizedTemplate{Subject: "For {{group_name}}", HTML: "<p>{{decision_reason}}</p>"},
		map[string]string{"group_name": "A & B", "decision_reason": "<script>alert(1)</script>"},
	)
	require.NoError(t, err)
	require.Equal(t, "For A &amp; B", subject)
	require.Equal(t, "<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>", body)
}

func TestExtractNewestGroupApplicationReplyOnlyRemovesQuotedHistory(t *testing.T) {
	require.Equal(t, "  EXACT-PHRASE", extractNewestGroupApplicationReply("  EXACT-PHRASE\r\n\r\nOn Tue, User wrote:\r\n> old text"))
	require.Equal(t, "EXACT-PHRASE\n-- \nMobile signature", extractNewestGroupApplicationReply("EXACT-PHRASE\n-- \nMobile signature"))
	require.Equal(t, "EXACT-PHRASE", extractNewestGroupApplicationReply("EXACT-PHRASE\n\n张三写道：\n> 历史内容"))
}

func TestParseGroupApplicationReplyExtractsCorrelationAndPlainText(t *testing.T) {
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: Re: Private Pro approval",
		"Message-ID: <reply@example.com>",
		"In-Reply-To: <approval@sub2api.local>",
		"References: <older@sub2api.local> <approval@sub2api.local>",
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM", "", "On Tue, Admin wrote:", "> old message",
	}, "\r\n")
	parsed, err := parseGroupApplicationReply([]byte(raw))
	require.NoError(t, err)
	require.Equal(t, "applicant@example.com", parsed.From)
	require.Equal(t, "<approval@sub2api.local>", parsed.InReplyTo)
	require.Contains(t, parsed.References, "<older@sub2api.local>")
	require.Equal(t, "Re: Private Pro approval", parsed.Subject)
	require.Equal(t, "CONFIRM", parsed.Reply)
}

type groupApplicationSettingRepoStub struct{ values map[string]string }

func (s *groupApplicationSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (s *groupApplicationSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}
func (s *groupApplicationSettingRepoStub) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}
func (s *groupApplicationSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("unused")
}
func (s *groupApplicationSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return errors.New("unused")
}
func (s *groupApplicationSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("unused")
}
func (s *groupApplicationSettingRepoStub) Delete(context.Context, string) error {
	return errors.New("unused")
}

type groupApplicationEncryptorStub struct{}

func (groupApplicationEncryptorStub) Encrypt(value string) (string, error) {
	return "enc:" + value, nil
}
func (groupApplicationEncryptorStub) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "enc:") {
		return "", errors.New("invalid ciphertext")
	}
	return strings.TrimPrefix(value, "enc:"), nil
}

func validGroupApplicationEmailConfig() GroupApplicationEmailConfig {
	return GroupApplicationEmailConfig{
		Enabled: true,
		SMTP: GroupApplicationSMTPConfig{
			Host: "smtp.example.com", Port: 587, Username: "sender@example.com", Password: "smtp-secret",
			FromAddress: "sender@example.com", FromName: "Applications", TLSMode: "starttls",
		},
		IMAP: GroupApplicationIMAPConfig{
			Host: "imap.example.com", Port: 993, Username: "inbox@example.com", Password: "imap-secret",
			Mailbox: "INBOX", ReplyAddress: "reply@example.com", TLSMode: "implicit", PollIntervalSeconds: 30,
		},
	}
}

func configuredGroupApplicationSettings(t *testing.T) *groupApplicationSettingRepoStub {
	t.Helper()
	repo := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	_, err := svc.SaveEmailConfig(context.Background(), validGroupApplicationEmailConfig())
	require.NoError(t, err)
	return repo
}

func TestGroupApplicationEmailConfigEncryptsMasksAndPreservesPasswords(t *testing.T) {
	repo := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.SaveEmailConfig(context.Background(), validGroupApplicationEmailConfig())
	require.NoError(t, err)
	require.True(t, public.SMTP.PasswordConfigured)
	require.True(t, public.IMAP.PasswordConfigured)
	require.Empty(t, public.SMTP.Password)
	require.Empty(t, public.IMAP.Password)
	require.NotContains(t, repo.values[SettingKeyGroupApplicationEmail], `"password":"smtp-secret"`)
	require.NotContains(t, repo.values[SettingKeyGroupApplicationEmail], `"password":"imap-secret"`)

	runtime, err := svc.LoadEmailConfig(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "smtp-secret", runtime.SMTP.Password)
	require.Equal(t, "imap-secret", runtime.IMAP.Password)

	public.SMTP.Password = ""
	public.IMAP.Password = "********"
	_, err = svc.SaveEmailConfig(context.Background(), *public)
	require.NoError(t, err)
	runtime, err = svc.LoadEmailConfig(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "smtp-secret", runtime.SMTP.Password)
	require.Equal(t, "imap-secret", runtime.IMAP.Password)

	public.SMTP.Host = "smtp2.example.com"
	_, err = svc.SaveEmailConfig(context.Background(), *public)
	require.ErrorContains(t, err, "enter the password again")
}

func TestGroupApplicationEmailConfigReusesSMTPIdentityWithoutDuplicatingPassword(t *testing.T) {
	repo := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	input := validGroupApplicationEmailConfig()
	input.IMAP.UseSMTPCredentials = true
	input.IMAP.Username = "ignored"
	input.IMAP.Password = "ignored-secret"
	public, err := svc.SaveEmailConfig(context.Background(), input)
	require.NoError(t, err)
	require.True(t, public.IMAP.PasswordConfigured)

	var stored storedGroupApplicationEmailConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyGroupApplicationEmail]), &stored))
	require.Empty(t, stored.IMAP.Username)
	require.Empty(t, stored.IMAP.EncryptedPassword)
	runtime, err := svc.LoadEmailConfig(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, runtime.SMTP.Username, runtime.IMAP.Username)
	require.Equal(t, runtime.SMTP.Password, runtime.IMAP.Password)
}

func TestGroupApplicationEmailConfigImportsLegacyIMAPWithoutEnablingWorkflow(t *testing.T) {
	legacy := storedGroupApplicationIMAPConfig{
		Enabled: true, Host: "legacy-imap.example.com", Port: 993, Username: "legacy@example.com",
		EncryptedPassword: "enc:legacy-secret", Mailbox: "Archive/Replies", ReplyAddress: "reply@example.com",
		TLSMode: "implicit", PollIntervalSeconds: 45,
	}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	repo := &groupApplicationSettingRepoStub{values: map[string]string{SettingKeyGroupApplicationIMAP: string(raw)}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})

	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)
	require.False(t, public.Enabled)
	require.True(t, public.LegacyImported)
	require.Equal(t, "legacy-imap.example.com", public.IMAP.Host)
	require.Equal(t, "Archive/Replies", public.IMAP.Mailbox)
	require.False(t, public.SMTP.PasswordConfigured)
	_, exists := repo.values[SettingKeyGroupApplicationEmail]
	require.False(t, exists)
}

func TestGroupApplicationEmailConfigRequiresBothTransportsWhenEnabled(t *testing.T) {
	repo := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	input := defaultGroupApplicationEmailConfig()
	input.Enabled = true
	_, err := svc.SaveEmailConfig(context.Background(), input)
	require.ErrorContains(t, err, "SMTP host")

	input.Enabled = false
	public, err := svc.SaveEmailConfig(context.Background(), input)
	require.NoError(t, err)
	require.False(t, public.Enabled)
}

func TestGroupApplicationTransportTestsIgnoreUnrelatedUnsavedCredentialChanges(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	public.IMAP.Host = "imap.unsaved.example.com"
	public.IMAP.Password = ""
	_, err = svc.ResolveEmailConfigForTest(context.Background(), *public, "smtp")
	require.NoError(t, err)

	public, err = svc.GetEmailConfig(context.Background())
	require.NoError(t, err)
	public.SMTP.Host = "smtp.unsaved.example.com"
	public.SMTP.Password = ""
	_, err = svc.ResolveEmailConfigForTest(context.Background(), *public, "imap")
	require.NoError(t, err)
}

func TestGroupApplicationMailboxNamesDeduplicateAndKeepInboxFirst(t *testing.T) {
	names := groupApplicationMailboxNames([]*imap.ListData{
		{Mailbox: "Archive/Applications"},
		{Mailbox: " INBOX "},
		{Mailbox: "Archive/Applications"},
		{Mailbox: "archive/applications"},
		{Mailbox: "Sent"},
		{Mailbox: "Container", Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}},
		{Mailbox: ""},
	})
	require.Equal(t, []string{"INBOX", "Archive/Applications", "Sent"}, names)
}

func TestGroupApplicationIMAPTestErrorIsActionableAndDoesNotExposeCause(t *testing.T) {
	tests := []struct {
		operation string
		reason    string
		contains  string
	}{
		{operation: "connect", reason: "GROUP_APPLICATION_IMAP_CONNECT_FAILED", contains: "connect"},
		{operation: "login", reason: "GROUP_APPLICATION_IMAP_LOGIN_FAILED", contains: "login"},
		{operation: "list", reason: "GROUP_APPLICATION_IMAP_LIST_FAILED", contains: "login succeeded"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			cause := errors.New("provider response containing sensitive-value")
			err := groupApplicationIMAPTestError(newGroupApplicationIMAPOperationError(test.operation, cause))
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, test.reason, infraerrors.Reason(err))
			require.Contains(t, strings.ToLower(infraerrors.Message(err)), test.contains)
			require.NotContains(t, infraerrors.Message(err), "sensitive-value")
		})
	}
}

type groupApplicationRepositoryStub struct {
	GroupApplicationRepository
	application      *GroupApplication
	jobs             []GroupApplicationMailJob
	completed        bool
	rejectedMismatch bool
	retried          bool
	retriedLastError string
	communications   []GroupApplicationCommunication
	claimCalls       int
	markSentCalls    int
	listOptionsCalls int
}

func (s *groupApplicationRepositoryStub) GetApplication(context.Context, int64) (*GroupApplication, error) {
	return s.application, nil
}

func (s *groupApplicationRepositoryStub) ListApplicationCommunications(context.Context, int64) ([]GroupApplicationCommunication, error) {
	return append([]GroupApplicationCommunication(nil), s.communications...), nil
}

func (s *groupApplicationRepositoryStub) CompleteFromReply(_ context.Context, _ int64, _ GroupApplicationMailJob) (*GroupApplication, error) {
	s.completed = true
	return s.application, nil
}

func (s *groupApplicationRepositoryStub) RejectReplyMismatch(_ context.Context, _ int64, _ GroupApplicationMailJob) (*GroupApplication, error) {
	s.rejectedMismatch = true
	return s.application, nil
}

func (s *groupApplicationRepositoryStub) ClaimMail(context.Context, string, int, time.Duration) ([]GroupApplicationMailJob, error) {
	s.claimCalls++
	return s.jobs, nil
}

func (s *groupApplicationRepositoryStub) MarkMailSent(context.Context, int64, string) error {
	s.markSentCalls++
	return nil
}

func (s *groupApplicationRepositoryStub) ListOptions(context.Context, int64) ([]GroupApplicationOption, error) {
	s.listOptionsCalls++
	return []GroupApplicationOption{{GroupID: 7, GroupName: "Private"}}, nil
}

func (s *groupApplicationRepositoryStub) RetryClaimedMail(_ context.Context, _ int64, _ string, _ time.Time, _ bool, lastError string) error {
	s.retried = true
	s.retriedLastError = lastError
	return nil
}

func TestGroupApplicationReplyRequiresExactVisibleText(t *testing.T) {
	application := &GroupApplication{
		ID: 1, ContactEmail: "applicant@example.com", GroupName: "Private Pro",
		ReplyPhraseSnapshot: "CONFIRM", TemplatesSnapshot: DefaultGroupApplicationTemplates(),
		CreatedAt: time.Now(),
	}
	repo := &groupApplicationRepositoryStub{application: application}
	svc := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})

	result, err := svc.ProcessInboundReply(context.Background(), application.ID, application.ContactEmail, " CONFIRM ")
	require.NoError(t, err)
	require.Equal(t, "reply_mismatch", result)
	require.True(t, repo.rejectedMismatch)
	require.False(t, repo.completed)

	repo.rejectedMismatch = false
	result, err = svc.ProcessInboundReply(context.Background(), application.ID, application.ContactEmail, "CONFIRM\r\n")
	require.NoError(t, err)
	require.Equal(t, "completed", result)
	require.True(t, repo.completed)
	require.False(t, repo.rejectedMismatch)
}

type groupApplicationEmailSenderStub struct {
	config  *SMTPConfig
	to      string
	options EmailSendOptions
	sends   int
}

func (s *groupApplicationEmailSenderStub) SendEmailWithConfigAndOptions(config *SMTPConfig, to, _, _ string, options EmailSendOptions) error {
	s.config = config
	s.to = to
	s.options = options
	s.sends++
	return nil
}

func (s *groupApplicationEmailSenderStub) TestSMTPConnectionWithConfig(*SMTPConfig) error { return nil }

func TestGroupApplicationWorkerPausesOutboxWhenWorkflowDisabled(t *testing.T) {
	repo := &groupApplicationRepositoryStub{jobs: []GroupApplicationMailJob{{
		ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
		Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
		MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
	}}}
	settings := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{repo: repo, service: svc, email: &groupApplicationEmailSenderStub{}, workerID: "test-worker"}

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Zero(t, repo.claimCalls)
	require.False(t, repo.retried)
}

func TestGroupApplicationWorkerUsesStandaloneSMTPAndReplyAddress(t *testing.T) {
	repo := &groupApplicationRepositoryStub{jobs: []GroupApplicationMailJob{{
		ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
		Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
		MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
	}}}
	settings := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	sender := &groupApplicationEmailSenderStub{}
	worker := &GroupApplicationWorker{repo: repo, service: svc, email: sender, workerID: "test-worker"}

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Equal(t, 1, repo.claimCalls)
	require.Equal(t, 1, repo.markSentCalls)
	require.Equal(t, 1, sender.sends)
	require.Equal(t, "smtp.example.com", sender.config.Host)
	require.Equal(t, "smtp-secret", sender.config.Password)
	require.Equal(t, "starttls", sender.config.TLSMode)
	require.Equal(t, "reply@example.com", sender.options.ReplyTo)
}

func TestGroupApplicationOptionsHiddenWhenDisabled(t *testing.T) {
	repo := &groupApplicationRepositoryStub{}
	settings := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	options, err := svc.ListOptions(context.Background(), 9)
	require.NoError(t, err)
	require.Empty(t, options)
	require.Zero(t, repo.listOptionsCalls)

	svc = NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	options, err = svc.ListOptions(context.Background(), 9)
	require.NoError(t, err)
	require.Len(t, options, 1)
	require.Equal(t, 1, repo.listOptionsCalls)
}

func TestGroupApplicationWorkflowMutationsAreBlockedWhenDisabled(t *testing.T) {
	repo := &groupApplicationRepositoryStub{}
	svc := NewGroupApplicationService(
		repo,
		&groupApplicationSettingRepoStub{values: map[string]string{}},
		groupApplicationEncryptorStub{},
	)

	checks := []func() error{
		func() error {
			_, err := svc.Submit(context.Background(), 1, 2, "user@example.com", "valid reason", "en")
			return err
		},
		func() error { _, err := svc.Approve(context.Background(), 1, 2); return err },
		func() error { _, err := svc.Reject(context.Background(), 1, 2, "reason"); return err },
		func() error { _, err := svc.Revoke(context.Background(), 1, 2, "reason"); return err },
		func() error { return svc.ResendApproval(context.Background(), 1) },
		func() error { return svc.RetryMail(context.Background(), 1, 2) },
	}
	for _, check := range checks {
		require.ErrorIs(t, check(), ErrGroupApplicationDisabled)
	}
}

func TestGroupApplicationCommunicationHistoryProtectsAndDecryptsInboundContent(t *testing.T) {
	application := &GroupApplication{ID: 17}
	repo := &groupApplicationRepositoryStub{application: application}
	svc := NewGroupApplicationService(repo, nil, groupApplicationEncryptorStub{})
	longReply := strings.Repeat("界", GroupApplicationMaxStoredReplyRunes+10)
	ciphertext, truncated, err := svc.protectInboundCommunication(" Re: approval ", longReply)
	require.NoError(t, err)
	require.True(t, truncated)
	require.True(t, strings.HasPrefix(ciphertext, "enc:"))

	repo.communications = []GroupApplicationCommunication{
		{
			ID: 1, ApplicationID: application.ID, Direction: GroupApplicationCommunicationOutbound,
			Subject: "Approved", HTMLBody: "<p>Reply now</p>", OccurredAt: time.Now(),
		},
		{
			ID: 2, ApplicationID: application.ID, Direction: GroupApplicationCommunicationInbound,
			EncryptedContent: ciphertext, ContentTruncated: truncated, Result: "reply_mismatch", OccurredAt: time.Now(),
		},
	}
	items, err := svc.ListCommunications(context.Background(), application.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "Re: approval", items[1].Subject)
	require.Len(t, []rune(items[1].TextBody), GroupApplicationMaxStoredReplyRunes)
	require.Empty(t, items[1].EncryptedContent)
	require.False(t, items[1].ContentUnavailable)

	encoded, err := json.Marshal(items)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "EncryptedContent")
	require.NotContains(t, string(encoded), "enc:")
}

func TestGroupApplicationCommunicationHistoryMarksLegacyAndUnreadableBodiesUnavailable(t *testing.T) {
	repo := &groupApplicationRepositoryStub{
		application: &GroupApplication{ID: 18},
		communications: []GroupApplicationCommunication{
			{ID: 1, ApplicationID: 18, Direction: GroupApplicationCommunicationInbound, OccurredAt: time.Now()},
			{ID: 2, ApplicationID: 18, Direction: GroupApplicationCommunicationInbound, EncryptedContent: "corrupt", OccurredAt: time.Now()},
		},
	}
	svc := NewGroupApplicationService(repo, nil, groupApplicationEncryptorStub{})
	items, err := svc.ListCommunications(context.Background(), 18)
	require.NoError(t, err)
	require.True(t, items[0].ContentUnavailable)
	require.True(t, items[1].ContentUnavailable)
	require.Empty(t, items[0].TextBody)
	require.Empty(t, items[1].TextBody)
}
