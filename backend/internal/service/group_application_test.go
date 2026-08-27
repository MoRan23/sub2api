//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestGroupApplicationIMAPConfigEncryptsAndMasksPassword(t *testing.T) {
	repo := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.SaveIMAPConfig(context.Background(), GroupApplicationIMAPConfig{Enabled: true, Host: "imap.example.com", Port: 993, Username: "inbox", Password: "secret", Mailbox: "INBOX", ReplyAddress: "reply@example.com", TLSMode: "implicit", PollIntervalSeconds: 30})
	require.NoError(t, err)
	require.True(t, public.PasswordConfigured)
	require.Empty(t, public.Password)
	require.NotContains(t, repo.values[SettingKeyGroupApplicationIMAP], `"secret"`)

	runtime, err := svc.LoadIMAPConfig(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "secret", runtime.Password)

	_, err = svc.SaveIMAPConfig(context.Background(), GroupApplicationIMAPConfig{Enabled: true, Host: "imap.example.com", Port: 993, Username: "inbox", Password: "********", Mailbox: "INBOX", ReplyAddress: "reply@example.com", TLSMode: "implicit", PollIntervalSeconds: 30})
	require.NoError(t, err)
	runtime, err = svc.LoadIMAPConfig(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "secret", runtime.Password)
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
	return s.jobs, nil
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
	svc := NewGroupApplicationService(repo, nil, nil)

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

func TestGroupApplicationApprovalMailWaitsForValidIMAPConfig(t *testing.T) {
	repo := &groupApplicationRepositoryStub{jobs: []GroupApplicationMailJob{{
		ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
		Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
		MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
	}}}
	settings := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{repo: repo, service: svc, workerID: "test-worker"}

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.True(t, repo.retried)
	require.Contains(t, repo.retriedLastError, "IMAP reply processing is disabled")
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
