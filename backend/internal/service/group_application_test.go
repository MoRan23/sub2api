//go:build unit

package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGroupApplicationTemplatesRequiresWorkflowPlaceholders(t *testing.T) {
	templates, err := NormalizeGroupApplicationTemplates(nil)
	require.NoError(t, err)
	require.Contains(t, templates[GroupApplicationMailApproval]["zh"].HTML, "{{reply_phrase}}")
	require.Contains(t, templates[GroupApplicationMailApproval]["zh"].HTML, "<!doctype html>")
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

func TestNormalizeGroupApplicationTemplatesUpgradesLegacyDefaults(t *testing.T) {
	legacy := legacyDefaultGroupApplicationTemplates()
	templates, err := NormalizeGroupApplicationTemplates(legacy)
	require.NoError(t, err)
	require.Contains(t, templates[GroupApplicationMailApproval]["zh"].HTML, "申请已通过初审")
	require.Contains(t, templates[GroupApplicationMailCompletion]["en"].HTML, "Access is now enabled")
	require.NotEqual(t, legacy[GroupApplicationMailApproval]["zh"].HTML, templates[GroupApplicationMailApproval]["zh"].HTML)
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

func TestGroupApplicationReplyForValidationAllowsOnlyLeadingPhraseAndSignature(t *testing.T) {
	tests := []struct {
		name, value, want string
	}{
		{name: "exact", value: "CONFIRM", want: "CONFIRM"},
		{name: "blank line signature", value: "CONFIRM\r\n\r\nHank\r\nhank@example.com", want: "CONFIRM"},
		{name: "standard signature delimiter", value: "CONFIRM\n-- \nHank", want: "CONFIRM"},
		{name: "extra body without separator", value: "CONFIRM\nplease also change the quota", want: "CONFIRM\nplease also change the quota"},
		{name: "phrase later", value: "Please approve\n\nCONFIRM", want: "Please approve"},
		{name: "indented delimiter", value: "CONFIRM\n -- \nHank", want: "CONFIRM\n -- \nHank"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, groupApplicationReplyForValidation(test.value))
		})
	}

	reply := extractNewestGroupApplicationReply("Not a confirmation\n\n> CONFIRM\n> quoted approval request")
	require.Equal(t, "Not a confirmation", groupApplicationReplyForValidation(reply))
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
	require.True(t, parsed.HasPlainText)
}

func TestParseGroupApplicationReplyMarksHTMLOnlyBodyUnsupported(t *testing.T) {
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: Re: approval",
		"Message-ID: <reply@example.com>",
		"In-Reply-To: <approval@sub2api.local>",
		"Content-Type: text/html; charset=UTF-8",
		"", "<p>CONFIRM</p>",
	}, "\r\n")

	parsed, err := parseGroupApplicationReply([]byte(raw))
	require.NoError(t, err)
	require.False(t, parsed.HasPlainText)
	require.Empty(t, parsed.Reply)
}

func TestParseGroupApplicationReplyDecodesRFC2047Subject(t *testing.T) {
	const want = "回复：审批通过"
	for _, test := range []struct {
		name, subject string
	}{
		{name: "base64", subject: mime.BEncoding.Encode("UTF-8", want)},
		{name: "quoted printable", subject: mime.QEncoding.Encode("UTF-8", want)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Join([]string{
				"From: Applicant <applicant@example.com>",
				"Subject: " + test.subject,
				"Content-Type: text/plain; charset=UTF-8",
				"", "CONFIRM",
			}, "\r\n")
			parsed, err := parseGroupApplicationReply([]byte(raw))
			require.NoError(t, err)
			require.Equal(t, want, parsed.Subject)
		})
	}

	const malformed = "=?UTF-8?B?%%%?="
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: " + malformed,
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM",
	}, "\r\n")
	parsed, err := parseGroupApplicationReply([]byte(raw))
	require.NoError(t, err)
	require.Equal(t, malformed, parsed.Subject)
}

func TestParseGroupApplicationReplySkipsEmptyPlainMultipartParts(t *testing.T) {
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: Re: approval",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="boundary"`,
		"",
		"--boundary",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"   ",
		"--boundary",
		"Content-Type: text/html; charset=UTF-8",
		"",
		"<p>CONFIRM</p>",
		"--boundary--",
	}, "\r\n")

	parsed, err := parseGroupApplicationReply([]byte(raw))
	require.NoError(t, err)
	require.False(t, parsed.HasPlainText)
	require.Empty(t, parsed.Reply)

	withLaterPlain := strings.Replace(raw, "--boundary--", strings.Join([]string{
		"--boundary",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"CONFIRM",
		"--boundary--",
	}, "\r\n"), 1)
	parsed, err = parseGroupApplicationReply([]byte(withLaterPlain))
	require.NoError(t, err)
	require.True(t, parsed.HasPlainText)
	require.Equal(t, "CONFIRM", parsed.Reply)
}

type groupApplicationApprovalLookupRepoStub struct {
	GroupApplicationRepository
	calls  []groupApplicationApprovalLookupCall
	lookup func([]string, []string) (*GroupApplicationApprovalMatch, error)
}

type groupApplicationApprovalLookupCall struct {
	exact, fallback []string
}

func (s *groupApplicationApprovalLookupRepoStub) FindApprovalByMessageIDs(_ context.Context, exact, fallback []string) (*GroupApplicationApprovalMatch, error) {
	call := groupApplicationApprovalLookupCall{
		exact:    append([]string(nil), exact...),
		fallback: append([]string(nil), fallback...),
	}
	s.calls = append(s.calls, call)
	return s.lookup(exact, fallback)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsSupportsEmbeddedInternalID(t *testing.T) {
	const canonical = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	want := &GroupApplicationApprovalMatch{Application: &GroupApplication{ID: 2}, MessageID: canonical}
	for _, test := range []struct {
		name      string
		rewritten string
		exact     []string
	}{
		{
			name:      "prefix inside angle brackets",
			rewritten: "<D487DA2F2221C5FD+group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>",
			exact:     []string{"<D487DA2F2221C5FD+group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"},
		},
		{
			name:      "suffix outside angle brackets",
			rewritten: "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>+D95D67DA1E433785",
			exact:     nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &groupApplicationApprovalLookupRepoStub{
				lookup: func(_, fallback []string) (*GroupApplicationApprovalMatch, error) {
					for _, messageID := range fallback {
						if messageID == canonical {
							return want, nil
						}
					}
					return nil, ErrGroupApplicationNotFound
				},
			}

			match, err := findGroupApplicationApprovalByReplyMessageIDs(context.Background(), repo, test.rewritten, test.rewritten)
			require.NoError(t, err)
			require.Same(t, want, match)
			require.Equal(t, []groupApplicationApprovalLookupCall{{exact: test.exact, fallback: []string{canonical}}}, repo.calls)
		})
	}
}

func TestFindGroupApplicationApprovalByReplyMessageIDsDeduplicatesExactAndEmbeddedMatch(t *testing.T) {
	const canonical = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	want := &GroupApplicationApprovalMatch{Application: &GroupApplication{ID: 2}, MessageID: canonical}
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, _ []string) (*GroupApplicationApprovalMatch, error) {
			return want, nil
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(context.Background(), repo, canonical, "")
	require.NoError(t, err)
	require.Same(t, want, match)
	require.Equal(t, []groupApplicationApprovalLookupCall{{exact: []string{canonical}}}, repo.calls)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsDoesNotFallbackOnRepositoryError(t *testing.T) {
	const rewritten = "<prefix+group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	wantErr := errors.New("database unavailable")
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, _ []string) (*GroupApplicationApprovalMatch, error) {
			return nil, wantErr
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(context.Background(), repo, rewritten, "")
	require.Nil(t, match)
	require.ErrorIs(t, err, wantErr)
	require.Len(t, repo.calls, 1)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsQueriesDeduplicatedCandidatesOnce(t *testing.T) {
	const canonical = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, _ []string) (*GroupApplicationApprovalMatch, error) {
			return nil, ErrGroupApplicationNotFound
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(context.Background(), repo, canonical, canonical)
	require.Nil(t, match)
	require.ErrorIs(t, err, ErrGroupApplicationNotFound)
	require.Equal(t, []groupApplicationApprovalLookupCall{{exact: []string{canonical}}}, repo.calls)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsAcceptsMultipleEmbeddedIDsForOneApplication(t *testing.T) {
	const first = "group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local"
	const second = "group-application-2-approval-29aa2182-7493-46a9-9075-118cc82c9203@sub2api.local"
	want := &GroupApplicationApprovalMatch{Application: &GroupApplication{ID: 2}, MessageID: "<" + second + ">"}
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, fallback []string) (*GroupApplicationApprovalMatch, error) {
			require.Contains(t, fallback, "<"+first+">")
			require.Contains(t, fallback, "<"+second+">")
			return want, nil
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(
		context.Background(), repo,
		"<provider+"+first+">",
		"<provider+"+first+"> <provider+"+second+">",
	)
	require.NoError(t, err)
	require.Same(t, want, match)
	require.Len(t, repo.calls, 1)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsPropagatesCrossApplicationAmbiguity(t *testing.T) {
	const first = "group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local"
	const second = "group-application-3-approval-29aa2182-7493-46a9-9075-118cc82c9203@sub2api.local"
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, fallback []string) (*GroupApplicationApprovalMatch, error) {
			require.Contains(t, fallback, "<"+first+">")
			require.Contains(t, fallback, "<"+second+">")
			return nil, ErrGroupApplicationReplyAmbiguous
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(
		context.Background(), repo,
		"<provider+"+first+">",
		"<provider+"+first+"> <provider+"+second+">",
	)
	require.Nil(t, match)
	require.ErrorIs(t, err, ErrGroupApplicationReplyAmbiguous)
	require.Len(t, repo.calls, 1)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsSeparatesExactAndEmbeddedCandidates(t *testing.T) {
	const first = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	const second = "<group-application-3-approval-29aa2182-7493-46a9-9075-118cc82c9203@sub2api.local>"
	providerWrappedSecond := "<provider+" + strings.Trim(second, "<>") + ">"
	want := &GroupApplicationApprovalMatch{Application: &GroupApplication{ID: 2}, MessageID: first}
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(exact, fallback []string) (*GroupApplicationApprovalMatch, error) {
			require.Equal(t, []string{first, providerWrappedSecond}, exact)
			require.Equal(t, []string{second}, fallback)
			return want, nil
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(
		context.Background(), repo,
		first,
		providerWrappedSecond,
	)
	require.NoError(t, err)
	require.Same(t, want, match)
	require.Len(t, repo.calls, 1)
}

func TestFindGroupApplicationApprovalByReplyMessageIDsDoesNotFallbackAfterExactAmbiguity(t *testing.T) {
	const canonical = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, _ []string) (*GroupApplicationApprovalMatch, error) {
			return nil, ErrGroupApplicationReplyAmbiguous
		},
	}

	match, err := findGroupApplicationApprovalByReplyMessageIDs(context.Background(), repo, canonical, "<other@example.com>")
	require.Nil(t, match)
	require.ErrorIs(t, err, ErrGroupApplicationReplyAmbiguous)
	require.Len(t, repo.calls, 1)
}

func TestParseGroupApplicationReplySupportsTrackedReferences(t *testing.T) {
	const canonical = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: Re: approval",
		"Message-ID: <tencent_reply@example.com>",
		"In-Reply-To: <unrelated@example.com>",
		"References: <older@example.com>,",
		"\t" + canonical + "+D95D67DA1E433785,",
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM",
	}, "\r\n")
	parsed, err := parseGroupApplicationReply([]byte(raw))
	require.NoError(t, err)

	want := &GroupApplicationApprovalMatch{Application: &GroupApplication{ID: 2}, MessageID: canonical}
	repo := &groupApplicationApprovalLookupRepoStub{
		lookup: func(_, fallback []string) (*GroupApplicationApprovalMatch, error) {
			require.Contains(t, fallback, canonical)
			return want, nil
		},
	}
	match, err := findGroupApplicationApprovalByReplyMessageIDs(context.Background(), repo, parsed.InReplyTo, parsed.References)
	require.NoError(t, err)
	require.Same(t, want, match)
}

func TestEmbeddedGroupApplicationApprovalMessageIDCandidatesRejectsLookalikes(t *testing.T) {
	valid := "prefix-group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local-suffix"
	require.Equal(t,
		[]string{"<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"},
		embeddedGroupApplicationApprovalMessageIDCandidates(valid),
	)
	require.Empty(t, embeddedGroupApplicationApprovalMessageIDCandidates(
		"<group-application-2-completion-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>",
		"<group-application-2-approval-3d579da3-3c63-3b1c-9684-954714939bd5@sub2api.local>",
		"<group-application-2-approval-3d579da3-3c63-4b1c-7684-954714939bd5@sub2api.local>",
		"<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@example.com>",
	))
}

func TestMessageIDCandidatesBoundsUntrustedHeaders(t *testing.T) {
	values := make([]string, 0, groupApplicationMaxMessageIDCandidates+10)
	for i := 0; i < groupApplicationMaxMessageIDCandidates+10; i++ {
		values = append(values, fmt.Sprintf("<message-%d@example.com>", i))
	}
	values = append([]string{"<" + strings.Repeat("x", groupApplicationMaxMessageIDTokenBytes) + ">"}, values...)

	candidates := messageIDCandidates(strings.Join(values, " "), "<ignored@example.com>")
	require.Len(t, candidates, groupApplicationMaxMessageIDCandidates)
	require.NotContains(t, candidates, "<ignored@example.com>")
	for _, candidate := range candidates {
		require.LessOrEqual(t, len(candidate), groupApplicationMaxMessageIDTokenBytes)
	}
	require.NotContains(t, messageIDCandidates("<bad\x00@example.com> <valid@example.com>", ""), "<bad\x00@example.com>")
}

func TestEmbeddedGroupApplicationApprovalMessageIDCandidatesDoesNotLetDuplicatesHideLaterIDs(t *testing.T) {
	const first = "group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local"
	const second = "group-application-2-approval-29aa2182-7493-46a9-9075-118cc82c9203@sub2api.local"

	header := strings.Repeat("<provider+"+first+"> ", groupApplicationMaxMessageIDCandidates+10) + "<provider+" + second + ">"
	require.Equal(t, []string{"<" + second + ">", "<" + first + ">"}, embeddedGroupApplicationApprovalMessageIDCandidates(header))
}

func TestGroupApplicationReceiptMetadataIsSafeForReceiptColumns(t *testing.T) {
	value := "prefix\x00" + strings.Repeat("界", groupApplicationMaxStoredMessageIDRunes+10)
	bounded := groupApplicationReceiptMetadata(value, groupApplicationMaxStoredMessageIDRunes)
	require.Len(t, []rune(bounded), groupApplicationMaxStoredMessageIDRunes)
	require.NotContains(t, bounded, "\x00")
	require.Equal(t, "invalid-\uFFFD-utf8", groupApplicationReceiptMetadata("invalid-\xff-utf8", 255))
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
	timeoutErr := groupApplicationIMAPTestError(newGroupApplicationIMAPOperationError("connect", context.DeadlineExceeded))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(timeoutErr))
	require.Equal(t, "GROUP_APPLICATION_IMAP_TIMEOUT", infraerrors.Reason(timeoutErr))
	require.Contains(t, infraerrors.Message(timeoutErr), "10-second")

	tests := []struct {
		operation string
		reason    string
		contains  string
	}{
		{operation: "connect", reason: "GROUP_APPLICATION_IMAP_CONNECT_FAILED", contains: "connect"},
		{operation: "login", reason: "GROUP_APPLICATION_IMAP_LOGIN_FAILED", contains: "login"},
		{operation: "list", reason: "GROUP_APPLICATION_IMAP_LIST_FAILED", contains: "login succeeded"},
		{operation: "select", reason: "GROUP_APPLICATION_IMAP_SELECT_FAILED", contains: "mailbox"},
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
	mails            []GroupApplicationMailStatus
	claimCalls       int
	claimLimit       int
	validateCalls    int
	validateErr      error
	markSentCalls    int
	listOptionsCalls int
	savedPolicy      *GroupApplicationPolicy
}

func (s *groupApplicationRepositoryStub) GetApplication(context.Context, int64) (*GroupApplication, error) {
	return s.application, nil
}

func (s *groupApplicationRepositoryStub) ListApplicationCommunications(context.Context, int64) ([]GroupApplicationCommunication, error) {
	return append([]GroupApplicationCommunication(nil), s.communications...), nil
}

func (s *groupApplicationRepositoryStub) ListApplicationMails(context.Context, int64) ([]GroupApplicationMailStatus, error) {
	return append([]GroupApplicationMailStatus(nil), s.mails...), nil
}

func (s *groupApplicationRepositoryStub) CompleteFromReply(_ context.Context, _ int64, _ GroupApplicationMailJob, _ *GroupApplicationReceipt) (*GroupApplication, error) {
	s.completed = true
	return s.application, nil
}

func (s *groupApplicationRepositoryStub) RejectReplyMismatch(_ context.Context, _ int64, _ GroupApplicationMailJob, _ *GroupApplicationReceipt) (*GroupApplication, error) {
	s.rejectedMismatch = true
	return s.application, nil
}

func (s *groupApplicationRepositoryStub) ClaimMail(_ context.Context, _ string, limit int, _ time.Duration) ([]GroupApplicationMailJob, error) {
	s.claimCalls++
	s.claimLimit = limit
	return s.jobs, nil
}

func (s *groupApplicationRepositoryStub) ValidateMailClaim(context.Context, int64, string) error {
	s.validateCalls++
	return s.validateErr
}

func (s *groupApplicationRepositoryStub) MarkMailSent(context.Context, int64, string) error {
	s.markSentCalls++
	return nil
}

func (s *groupApplicationRepositoryStub) ListOptions(context.Context, int64) ([]GroupApplicationOption, error) {
	s.listOptionsCalls++
	return []GroupApplicationOption{{GroupID: 7, GroupName: "Private"}}, nil
}

func (s *groupApplicationRepositoryStub) SavePolicy(_ context.Context, policy *GroupApplicationPolicy, _ *GroupApplicationAttachment, _ int64) (*GroupApplicationPolicy, error) {
	s.savedPolicy = policy
	return policy, nil
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

	result, err := svc.ProcessInboundReply(context.Background(), application.ID, application.ContactEmail, " CONFIRM ", nil)
	require.NoError(t, err)
	require.Equal(t, "reply_mismatch", result)
	require.True(t, repo.rejectedMismatch)
	require.False(t, repo.completed)

	repo.rejectedMismatch = false
	result, err = svc.ProcessInboundReply(context.Background(), application.ID, application.ContactEmail, "CONFIRM\r\n", nil)
	require.NoError(t, err)
	require.Equal(t, "completed", result)
	require.True(t, repo.completed)
	require.False(t, repo.rejectedMismatch)
}

func TestGroupApplicationGetApplicationDerivesReplyStatusForEveryApprovalMail(t *testing.T) {
	for _, test := range []struct {
		name              string
		applicationStatus string
		sentReplyStatus   string
		failedReplyStatus string
		failedRetryable   bool
		completionActive  bool
	}{
		{
			name:              "awaiting reply",
			applicationStatus: GroupApplicationStatusAwaitingReply,
			sentReplyStatus:   GroupApplicationReplyStatusAwaitingReply,
			failedRetryable:   true,
		},
		{
			name:              "completed",
			applicationStatus: GroupApplicationStatusCompleted,
			sentReplyStatus:   GroupApplicationReplyStatusCompleted,
			failedReplyStatus: GroupApplicationReplyStatusCompleted,
			completionActive:  true,
		},
		{
			name:              "rejected",
			applicationStatus: GroupApplicationStatusRejected,
			sentReplyStatus:   GroupApplicationReplyStatusCompleted,
			failedReplyStatus: GroupApplicationReplyStatusCompleted,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &groupApplicationRepositoryStub{
				application: &GroupApplication{ID: 17, Status: test.applicationStatus},
				mails: []GroupApplicationMailStatus{
					{ID: 1, Kind: GroupApplicationMailApproval, Status: "sent", RequiredStatus: GroupApplicationStatusAwaitingReply},
					{ID: 2, Kind: GroupApplicationMailApproval, Status: "failed", RequiredStatus: GroupApplicationStatusAwaitingReply},
					{ID: 3, Kind: GroupApplicationMailCompletion, Status: "processing", RequiredStatus: GroupApplicationStatusCompleted},
				},
			}
			svc := NewGroupApplicationService(repo, nil, nil)

			application, err := svc.GetApplication(context.Background(), 17)
			require.NoError(t, err)
			require.Equal(t, test.sentReplyStatus, application.Mails[0].ReplyStatus)
			require.Equal(t, test.failedReplyStatus, application.Mails[1].ReplyStatus)
			require.Empty(t, application.Mails[2].ReplyStatus)
			require.Equal(t, test.failedRetryable, application.Mails[1].Retryable)
			require.Equal(t, test.completionActive, application.Mails[2].DeliveryActive)
			require.Equal(t, "sent", application.Mails[0].Status)
			require.Equal(t, "failed", application.Mails[1].Status)
		})
	}
}

func TestGroupApplicationListCommunicationsCompletesAllApprovalReplyThreadsAfterRejection(t *testing.T) {
	repo := &groupApplicationRepositoryStub{
		application: &GroupApplication{ID: 18, Status: GroupApplicationStatusRejected},
		communications: []GroupApplicationCommunication{
			{ID: 1, Direction: GroupApplicationCommunicationOutbound, Kind: GroupApplicationMailApproval, Status: "sent", RequiredStatus: GroupApplicationStatusAwaitingReply},
			{ID: 2, Direction: GroupApplicationCommunicationOutbound, Kind: GroupApplicationMailApproval, Status: "failed", RequiredStatus: GroupApplicationStatusAwaitingReply},
			{ID: 3, Direction: GroupApplicationCommunicationOutbound, Kind: GroupApplicationMailManualRejection, Status: "processing", RequiredStatus: GroupApplicationStatusRejected},
			{ID: 4, Direction: GroupApplicationCommunicationOutbound, Kind: GroupApplicationMailCompletion, Status: "failed", RequiredStatus: GroupApplicationStatusCompleted},
		},
	}
	svc := NewGroupApplicationService(repo, nil, nil)

	items, err := svc.ListCommunications(context.Background(), 18)
	require.NoError(t, err)
	require.Equal(t, GroupApplicationReplyStatusCompleted, items[0].ReplyStatus)
	require.Equal(t, GroupApplicationReplyStatusCompleted, items[1].ReplyStatus)
	require.Empty(t, items[2].ReplyStatus)
	require.False(t, items[1].Retryable)
	require.True(t, items[2].DeliveryActive)
	require.False(t, items[3].Retryable)
	require.Equal(t, []string{"sent", "failed", "processing", "failed"}, []string{items[0].Status, items[1].Status, items[2].Status, items[3].Status})
}

type groupApplicationEmailSenderStub struct {
	config  *SMTPConfig
	to      string
	options EmailSendOptions
	sends   int
	err     error
}

func (s *groupApplicationEmailSenderStub) SendEmailWithConfigAndOptions(config *SMTPConfig, to, _, _ string, options EmailSendOptions) error {
	s.config = config
	s.to = to
	s.options = options
	s.sends++
	return s.err
}

func (s *groupApplicationEmailSenderStub) TestSMTPConnectionWithConfig(*SMTPConfig) error { return nil }

type groupApplicationWorkerRepoStub struct {
	GroupApplicationRepository
	jobs        []GroupApplicationMailJob
	claimGate   <-chan struct{}
	claimErr    error
	claimCalled chan struct{}
	mailSent    chan struct{}
	claimOnce   sync.Once
	mailOnce    sync.Once
	mu          sync.Mutex
	claimed     bool
}

func (s *groupApplicationWorkerRepoStub) ClaimMail(ctx context.Context, _ string, _ int, _ time.Duration) ([]GroupApplicationMailJob, error) {
	if s.claimCalled != nil {
		s.claimOnce.Do(func() { close(s.claimCalled) })
	}
	if s.claimGate != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.claimGate:
		}
	}
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return append([]GroupApplicationMailJob(nil), s.jobs...), nil
}

func (s *groupApplicationWorkerRepoStub) MarkMailSent(context.Context, int64, string) error {
	if s.mailSent != nil {
		s.mailOnce.Do(func() { close(s.mailSent) })
	}
	return nil
}

func (s *groupApplicationWorkerRepoStub) ValidateMailClaim(context.Context, int64, string) error {
	return nil
}

func TestGroupApplicationWorkerBlockedIMAPDoesNotStarveMailOutbox(t *testing.T) {
	pollStarted := make(chan struct{})
	mailSent := make(chan struct{})
	repo := &groupApplicationWorkerRepoStub{
		jobs: []GroupApplicationMailJob{{
			ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
			Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
			MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
		}},
		claimGate: pollStarted,
		mailSent:  mailSent,
	}
	settings := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	sender := &groupApplicationEmailSenderStub{}
	worker := newGroupApplicationWorker(repo, svc, sender)
	var pollOnce sync.Once
	worker.imapPoller = func(ctx context.Context, _ *GroupApplicationIMAPConfig) error {
		pollOnce.Do(func() { close(pollStarted) })
		<-ctx.Done()
		return ctx.Err()
	}
	worker.Start()
	t.Cleanup(worker.Stop)

	select {
	case <-pollStarted:
	case <-time.After(time.Second):
		t.Fatal("IMAP poller did not start")
	}
	select {
	case <-mailSent:
	case <-time.After(time.Second):
		t.Fatal("mail outbox was starved by the blocked IMAP poller")
	}

	worker.Stop()
	health := worker.Health()
	require.False(t, health.Running)
	require.Equal(t, uint64(1), health.MailProcessed)
	require.False(t, health.LastMailCheckAt.IsZero())
	require.Empty(t, health.LastMailError)
	require.Equal(t, 1, sender.sends)
}

type groupApplicationIMAPPollLiteral struct {
	*strings.Reader
	size int64
}

func newGroupApplicationIMAPPollLiteral(value string) *groupApplicationIMAPPollLiteral {
	return &groupApplicationIMAPPollLiteral{Reader: strings.NewReader(value), size: int64(len(value))}
}

func (r *groupApplicationIMAPPollLiteral) Size() int64 { return r.size }

type groupApplicationIMAPPollRepoStub struct {
	GroupApplicationRepository
	application        *GroupApplication
	validMessageID     string
	ambiguousMessageID map[string]struct{}
	receipts           []GroupApplicationReceipt
	completed          bool
	rejectedMismatch   bool
	completeErr        error
	terminalReceiptErr error
	completeCalls      int
	maxUID             uint32
	cursorExists       bool
}

func (s *groupApplicationIMAPPollRepoStub) MaxProcessedUID(context.Context, string, uint32) (uint32, bool, error) {
	return s.maxUID, s.cursorExists, nil
}

func (s *groupApplicationIMAPPollRepoStub) StoreReceipt(_ context.Context, receipt GroupApplicationReceipt) (bool, error) {
	s.receipts = append(s.receipts, receipt)
	if receipt.UID > s.maxUID {
		s.maxUID = receipt.UID
	}
	s.cursorExists = true
	return true, nil
}

func (s *groupApplicationIMAPPollRepoStub) FindApprovalByMessageIDs(_ context.Context, exact, fallback []string) (*GroupApplicationApprovalMatch, error) {
	for _, messageID := range exact {
		if messageID == s.validMessageID {
			return &GroupApplicationApprovalMatch{Application: s.application, MessageID: s.validMessageID}, nil
		}
	}
	for _, messageID := range fallback {
		if messageID == s.validMessageID {
			return &GroupApplicationApprovalMatch{Application: s.application, MessageID: s.validMessageID}, nil
		}
	}
	matchedAmbiguous := 0
	for _, messageID := range fallback {
		if _, ok := s.ambiguousMessageID[messageID]; ok {
			matchedAmbiguous++
		}
	}
	if len(s.ambiguousMessageID) > 0 && matchedAmbiguous == len(s.ambiguousMessageID) {
		return nil, ErrGroupApplicationReplyAmbiguous
	}
	return nil, ErrGroupApplicationNotFound
}

func (s *groupApplicationIMAPPollRepoStub) GetApplication(context.Context, int64) (*GroupApplication, error) {
	return s.application, nil
}

func (s *groupApplicationIMAPPollRepoStub) CompleteFromReply(ctx context.Context, _ int64, _ GroupApplicationMailJob, receipt *GroupApplicationReceipt) (*GroupApplication, error) {
	s.completeCalls++
	if s.completeErr != nil {
		return nil, s.completeErr
	}
	if s.terminalReceiptErr != nil {
		return nil, s.terminalReceiptErr
	}
	if receipt != nil {
		if _, err := s.StoreReceipt(ctx, *receipt); err != nil {
			return nil, err
		}
	}
	s.completed = true
	return s.application, nil
}

func (s *groupApplicationIMAPPollRepoStub) RejectReplyMismatch(ctx context.Context, _ int64, _ GroupApplicationMailJob, receipt *GroupApplicationReceipt) (*GroupApplication, error) {
	if s.terminalReceiptErr != nil {
		return nil, s.terminalReceiptErr
	}
	if receipt != nil {
		if _, err := s.StoreReceipt(ctx, *receipt); err != nil {
			return nil, err
		}
	}
	s.rejectedMismatch = true
	return s.application, nil
}

var (
	groupApplicationIMAPPollTLSOnce sync.Once
	groupApplicationIMAPPollTLSCert tls.Certificate
)

func groupApplicationIMAPPollCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	groupApplicationIMAPPollTLSOnce.Do(func() {
		certificate, roots := newSMTPTestCert(t)
		groupApplicationIMAPPollTLSCert = certificate
		x509.SetFallbackRoots(roots)
	})
	return groupApplicationIMAPPollTLSCert
}

type groupApplicationIMAPFetchCall struct {
	set          string
	rfc822Size   bool
	bodySections int
	partialSizes []int64
}

type groupApplicationIMAPFetchTrace struct {
	mu    sync.Mutex
	calls []groupApplicationIMAPFetchCall
}

func (t *groupApplicationIMAPFetchTrace) snapshot() []groupApplicationIMAPFetchCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	calls := make([]groupApplicationIMAPFetchCall, len(t.calls))
	for i := range t.calls {
		calls[i] = t.calls[i]
		calls[i].partialSizes = append([]int64(nil), t.calls[i].partialSizes...)
	}
	return calls
}

type groupApplicationIMAPTracingSession struct {
	imapserver.Session
	trace *groupApplicationIMAPFetchTrace
}

func (s *groupApplicationIMAPTracingSession) Fetch(w *imapserver.FetchWriter, set imap.NumSet, options *imap.FetchOptions) error {
	call := groupApplicationIMAPFetchCall{set: set.String()}
	if options != nil {
		call.rfc822Size = options.RFC822Size
		call.bodySections = len(options.BodySection)
		for _, section := range options.BodySection {
			if section.Partial != nil {
				call.partialSizes = append(call.partialSizes, section.Partial.Size)
			}
		}
	}
	s.trace.mu.Lock()
	s.trace.calls = append(s.trace.calls, call)
	s.trace.mu.Unlock()
	return s.Session.Fetch(w, set, options)
}

func startGroupApplicationIMAPTestServer(t *testing.T, messages ...string) (GroupApplicationIMAPConfig, *groupApplicationIMAPFetchTrace) {
	t.Helper()
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	certificate := groupApplicationIMAPPollCertificate(t)
	backend := imapmemserver.New()
	user := imapmemserver.NewUser("inbox@example.com", "secret")
	require.NoError(t, user.Create("INBOX", nil))
	for _, raw := range messages {
		_, err := user.Append("INBOX", newGroupApplicationIMAPPollLiteral(raw), &imap.AppendOptions{})
		require.NoError(t, err)
	}
	backend.AddUser(user)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"imap"},
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	require.NoError(t, err)
	trace := &groupApplicationIMAPFetchTrace{}
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &groupApplicationIMAPTracingSession{Session: backend.NewSession(), trace: trace}, nil, nil
		},
		TLSConfig: tlsConfig,
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, <-serveDone)
	})

	return GroupApplicationIMAPConfig{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
		Username: "inbox@example.com", Password: "secret", Mailbox: "INBOX",
		ReplyAddress: "reply@example.com", TLSMode: "implicit", PollIntervalSeconds: 30,
	}, trace
}

func TestGroupApplicationTestIMAPSelectsConfiguredMailbox(t *testing.T) {
	cfg, _ := startGroupApplicationIMAPTestServer(t)
	input := validGroupApplicationEmailConfig()
	input.IMAP = cfg
	service := NewGroupApplicationService(nil, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{service: service}

	names, err := worker.TestIMAP(context.Background(), input)
	require.NoError(t, err)
	require.Contains(t, names, "INBOX")

	input.IMAP.Mailbox = "Missing"
	names, err = worker.TestIMAP(context.Background(), input)
	require.Nil(t, names)
	require.Error(t, err)
	require.Equal(t, "GROUP_APPLICATION_IMAP_SELECT_FAILED", infraerrors.Reason(err))
}

func TestGroupApplicationIMAPHighestUIDFallsBackWhenUIDNextIsZero(t *testing.T) {
	raw := "From: sender@example.com\r\nContent-Type: text/plain\r\n\r\nbody"
	cfg, trace := startGroupApplicationIMAPTestServer(t, raw)
	client, selected, err := openGroupApplicationMailbox(context.Background(), &cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	selected.UIDNext = 0

	highest, err := groupApplicationIMAPHighestUID(client, selected)
	require.NoError(t, err)
	require.Equal(t, uint32(1), highest)
	require.Contains(t, trace.snapshot(), groupApplicationIMAPFetchCall{set: "1"})
}

func TestGroupApplicationPollIMAPChecksSizeBeforeFetchingLargeBody(t *testing.T) {
	raw := "From: sender@example.com\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", groupApplicationIMAPMaxMessageBytes)
	cfg, trace := startGroupApplicationIMAPTestServer(t, raw)
	repo := &groupApplicationIMAPPollRepoStub{}
	worker := &GroupApplicationWorker{repo: repo}

	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.Len(t, repo.receipts, 2)
	require.Equal(t, GroupApplicationReceipt{MailboxFingerprint: groupApplicationMailboxFingerprint(&cfg), UIDValidity: repo.receipts[0].UIDValidity, UID: 0, Result: "cursor_start"}, repo.receipts[0])
	require.Equal(t, "too_large", repo.receipts[1].Result)
	calls := trace.snapshot()
	require.NotEmpty(t, calls)
	require.True(t, calls[0].rfc822Size)
	for _, call := range calls {
		require.Zero(t, call.bodySections)
	}
}

func TestGroupApplicationPollIMAPRecordsAutomatedReplyWithoutCompletingApplication(t *testing.T) {
	const approvalMessageID = "<group-application-42-approval-b9ac73ed-9a79-40f9-a620-167ae80bdbd7@sub2api.local>"
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: automatic reply",
		"Message-ID: <automatic@example.com>",
		"In-Reply-To: " + approvalMessageID,
		"Auto-Submitted: auto-replied",
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM",
	}, "\r\n")
	cfg, trace := startGroupApplicationIMAPTestServer(t, raw)
	repo := &groupApplicationIMAPPollRepoStub{
		application:    &GroupApplication{ID: 42, ContactEmail: "applicant@example.com", ReplyPhraseSnapshot: "CONFIRM"},
		validMessageID: approvalMessageID,
	}
	worker := &GroupApplicationWorker{repo: repo}

	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.Len(t, repo.receipts, 2)
	require.Equal(t, "cursor_start", repo.receipts[0].Result)
	require.Equal(t, "automated", repo.receipts[1].Result)
	require.False(t, repo.completed)
	require.Empty(t, repo.receipts[1].EncryptedContent)
	var bodyCalls []groupApplicationIMAPFetchCall
	for _, call := range trace.snapshot() {
		if call.bodySections > 0 {
			bodyCalls = append(bodyCalls, call)
		}
	}
	require.Equal(t, []groupApplicationIMAPFetchCall{{set: "1", bodySections: 1, partialSizes: []int64{groupApplicationIMAPMaxMessageBytes + 1}}}, bodyCalls)
}

func TestGroupApplicationPollIMAPStoresStateConflictAndContinues(t *testing.T) {
	const approvalMessageID = "<group-application-42-approval-b9ac73ed-9a79-40f9-a620-167ae80bdbd7@sub2api.local>"
	correlated := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: valid reply during state transition",
		"Message-ID: <state-conflict@example.com>",
		"In-Reply-To: " + approvalMessageID,
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM",
	}, "\r\n")
	unrelated := strings.Join([]string{
		"From: sender@example.com",
		"Subject: unrelated",
		"Message-ID: <unrelated@example.com>",
		"Content-Type: text/plain; charset=UTF-8",
		"", "hello",
	}, "\r\n")
	cfg, _ := startGroupApplicationIMAPTestServer(t, correlated, unrelated)
	application := &GroupApplication{
		ID: 42, ContactEmail: "applicant@example.com", GroupName: "Private Pro",
		ReplyPhraseSnapshot: "CONFIRM", TemplatesSnapshot: DefaultGroupApplicationTemplates(), CreatedAt: time.Now(),
	}
	repo := &groupApplicationIMAPPollRepoStub{
		application: application, validMessageID: approvalMessageID, completeErr: ErrGroupApplicationState,
	}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{repo: repo, service: service}

	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.Len(t, repo.receipts, 3)
	require.Equal(t, []string{"cursor_start", "state_conflict", "unrelated"}, []string{repo.receipts[0].Result, repo.receipts[1].Result, repo.receipts[2].Result})
	require.False(t, repo.completed)
	require.Equal(t, uint32(2), repo.maxUID)
}

func TestGroupApplicationPollIMAPRetriesWhenAtomicReceiptStoreFails(t *testing.T) {
	const approvalMessageID = "<group-application-42-approval-b9ac73ed-9a79-40f9-a620-167ae80bdbd7@sub2api.local>"
	raw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: valid reply",
		"Message-ID: <retry-receipt@example.com>",
		"In-Reply-To: " + approvalMessageID,
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM",
	}, "\r\n")
	cfg, _ := startGroupApplicationIMAPTestServer(t, raw)
	receiptErr := errors.New("receipt database unavailable")
	application := &GroupApplication{
		ID: 42, ContactEmail: "applicant@example.com", GroupName: "Private Pro",
		ReplyPhraseSnapshot: "CONFIRM", TemplatesSnapshot: DefaultGroupApplicationTemplates(), CreatedAt: time.Now(),
	}
	repo := &groupApplicationIMAPPollRepoStub{
		application: application, validMessageID: approvalMessageID, terminalReceiptErr: receiptErr,
	}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{repo: repo, service: service}

	err := worker.pollIMAP(context.Background(), &cfg)
	require.ErrorIs(t, err, receiptErr)
	require.False(t, repo.completed)
	require.Equal(t, 1, repo.completeCalls)
	require.Len(t, repo.receipts, 1)
	require.Equal(t, "cursor_start", repo.receipts[0].Result)
	require.Equal(t, uint32(0), repo.maxUID)

	repo.terminalReceiptErr = nil
	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.True(t, repo.completed)
	require.Equal(t, 2, repo.completeCalls)
	require.Len(t, repo.receipts, 2)
	require.Equal(t, "completed", repo.receipts[1].Result)
	require.Equal(t, uint32(1), repo.maxUID)
}

func TestGroupApplicationPollIMAPLeavesMessagesBeyondPerPollLimitForNextPoll(t *testing.T) {
	messages := make([]string, groupApplicationIMAPMaxMessagesPerPoll+1)
	for i := range messages {
		messages[i] = fmt.Sprintf("From: sender@example.com\r\nSubject: unrelated %d\r\nMessage-ID: <unrelated-%d@example.com>\r\nContent-Type: text/plain\r\n\r\nhello", i, i)
	}
	cfg, _ := startGroupApplicationIMAPTestServer(t, messages...)
	repo := &groupApplicationIMAPPollRepoStub{}
	worker := &GroupApplicationWorker{repo: repo}

	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.Len(t, repo.receipts, groupApplicationIMAPMaxMessagesPerPoll+1)
	require.Equal(t, "cursor_start", repo.receipts[0].Result)
	require.Equal(t, uint32(0), repo.receipts[0].UID)
	require.Equal(t, uint32(1), repo.receipts[1].UID)
	require.Equal(t, uint32(groupApplicationIMAPMaxMessagesPerPoll), repo.maxUID)
	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.Len(t, repo.receipts, groupApplicationIMAPMaxMessagesPerPoll+2)
	require.Equal(t, uint32(groupApplicationIMAPMaxMessagesPerPoll+1), repo.maxUID)
}

func TestGroupApplicationIMAPBootstrapUIDKeepsSmallMailboxAndBoundsLargeMailbox(t *testing.T) {
	tests := []struct {
		name    string
		highest uint32
		want    uint32
	}{
		{name: "empty", highest: 0, want: 0},
		{name: "small", highest: groupApplicationIMAPBootstrapWindow, want: 0},
		{name: "first over window", highest: groupApplicationIMAPBootstrapWindow + 1, want: 1},
		{name: "large", highest: groupApplicationIMAPBootstrapWindow + 400, want: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, groupApplicationIMAPBootstrapUID(test.highest))
		})
	}
}

func TestGroupApplicationPollIMAPBootstrapWindowBoundsLargeMailboxHistory(t *testing.T) {
	messages := make([]string, int(groupApplicationIMAPBootstrapWindow)+1)
	for i := range messages {
		messages[i] = fmt.Sprintf("From: sender@example.com\r\nSubject: historical %d\r\nMessage-ID: <historical-%d@example.com>\r\nContent-Type: text/plain\r\n\r\nhello", i, i)
	}
	cfg, trace := startGroupApplicationIMAPTestServer(t, messages...)
	repo := &groupApplicationIMAPPollRepoStub{}
	worker := &GroupApplicationWorker{repo: repo}

	require.NoError(t, worker.pollIMAP(context.Background(), &cfg))
	require.Len(t, repo.receipts, groupApplicationIMAPMaxMessagesPerPoll+1)
	require.Equal(t, "cursor_start", repo.receipts[0].Result)
	require.Equal(t, uint32(1), repo.receipts[0].UID)
	require.Equal(t, uint32(2), repo.receipts[1].UID)
	require.Equal(t, uint32(groupApplicationIMAPMaxMessagesPerPoll+1), repo.receipts[len(repo.receipts)-1].UID)
	require.Equal(t, uint32(groupApplicationIMAPMaxMessagesPerPoll+1), repo.maxUID)
	bodySets := make([]string, 0, groupApplicationIMAPMaxMessagesPerPoll)
	for _, call := range trace.snapshot() {
		if call.bodySections > 0 {
			bodySets = append(bodySets, call.set)
		}
	}
	require.Len(t, bodySets, groupApplicationIMAPMaxMessagesPerPoll)
	require.Equal(t, "2", bodySets[0])
	require.NotContains(t, bodySets, "1")
}

func TestGroupApplicationMailboxFingerprintIncludesPortAndUsernameCase(t *testing.T) {
	base := GroupApplicationIMAPConfig{Host: "IMAP.EXAMPLE.COM", Port: 993, Username: "MailboxUser", Mailbox: "INBOX"}
	hostCase := base
	hostCase.Host = strings.ToLower(hostCase.Host)
	require.Equal(t, groupApplicationMailboxFingerprint(&base), groupApplicationMailboxFingerprint(&hostCase))

	differentPort := base
	differentPort.Port++
	require.NotEqual(t, groupApplicationMailboxFingerprint(&base), groupApplicationMailboxFingerprint(&differentPort))
	differentUsernameCase := base
	differentUsernameCase.Username = strings.ToLower(differentUsernameCase.Username)
	require.NotEqual(t, groupApplicationMailboxFingerprint(&base), groupApplicationMailboxFingerprint(&differentUsernameCase))
}

func TestGroupApplicationPollIMAPConsumesAmbiguousLongHeaderBeforeValidReply(t *testing.T) {
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	certificate := groupApplicationIMAPPollCertificate(t)

	const (
		firstAmbiguous  = "<group-application-2-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
		secondAmbiguous = "<group-application-3-approval-29aa2182-7493-46a9-9075-118cc82c9203@sub2api.local>"
		valid           = "<group-application-42-approval-b9ac73ed-9a79-40f9-a620-167ae80bdbd7@sub2api.local>"
	)
	rewrite := func(messageID string) string {
		return "<" + strings.Repeat("provider-opaque-", 20) + "+" + strings.Trim(messageID, "<>") + ">"
	}
	firstRaw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: ambiguous provider rewrite",
		"Message-ID: <" + strings.Repeat("r", 300) + "@example.com>",
		"In-Reply-To: " + rewrite(firstAmbiguous),
		"References: " + rewrite(firstAmbiguous),
		"\t" + rewrite(secondAmbiguous),
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM",
	}, "\r\n")
	secondRaw := strings.Join([]string{
		"From: Applicant <applicant@example.com>",
		"Subject: valid reply",
		"Message-ID: <valid-reply@example.com>",
		"In-Reply-To: " + valid,
		"Content-Type: text/plain; charset=UTF-8",
		"", "CONFIRM", "", "Hank", "hank@example.com",
	}, "\r\n")

	backend := imapmemserver.New()
	user := imapmemserver.NewUser("inbox@example.com", "secret")
	require.NoError(t, user.Create("INBOX", nil))
	_, err := user.Append("INBOX", newGroupApplicationIMAPPollLiteral(firstRaw), &imap.AppendOptions{})
	require.NoError(t, err)
	_, err = user.Append("INBOX", newGroupApplicationIMAPPollLiteral(secondRaw), &imap.AppendOptions{})
	require.NoError(t, err)
	backend.AddUser(user)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"imap"},
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	require.NoError(t, err)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return backend.NewSession(), nil, nil
		},
		TLSConfig: tlsConfig,
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, <-serveDone)
	})

	application := &GroupApplication{
		ID:                  42,
		ContactEmail:        "applicant@example.com",
		GroupName:           "Private Pro",
		ReplyPhraseSnapshot: "CONFIRM",
		TemplatesSnapshot:   DefaultGroupApplicationTemplates(),
		CreatedAt:           time.Now(),
	}
	repo := &groupApplicationIMAPPollRepoStub{
		application:    application,
		validMessageID: valid,
		ambiguousMessageID: map[string]struct{}{
			firstAmbiguous:  {},
			secondAmbiguous: {},
		},
	}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{repo: repo, service: service}
	port := listener.Addr().(*net.TCPAddr).Port

	require.NoError(t, worker.pollIMAP(context.Background(), &GroupApplicationIMAPConfig{
		Host: "127.0.0.1", Port: port, Username: "inbox@example.com", Password: "secret",
		Mailbox: "INBOX", TLSMode: "implicit",
	}))

	require.Len(t, repo.receipts, 3)
	require.Equal(t, []uint32{0, 1, 2}, []uint32{repo.receipts[0].UID, repo.receipts[1].UID, repo.receipts[2].UID})
	require.Equal(t, "cursor_start", repo.receipts[0].Result)
	require.Equal(t, "ambiguous", repo.receipts[1].Result)
	require.Len(t, []rune(repo.receipts[1].MessageID), groupApplicationMaxStoredMessageIDRunes)
	require.Len(t, []rune(repo.receipts[1].InReplyTo), groupApplicationMaxStoredMessageIDRunes)
	require.Equal(t, "completed", repo.receipts[2].Result)
	require.NotNil(t, repo.receipts[2].ApplicationID)
	require.Equal(t, application.ID, *repo.receipts[2].ApplicationID)
	require.True(t, repo.completed)
	require.Equal(t, uint64(1), worker.repliesProcessed.Load())
	decrypted, err := groupApplicationEncryptorStub{}.Decrypt(repo.receipts[2].EncryptedContent)
	require.NoError(t, err)
	var stored groupApplicationInboundContent
	require.NoError(t, json.Unmarshal([]byte(decrypted), &stored))
	require.Equal(t, "CONFIRM\n\nHank\nhank@example.com", stored.Text)
}

func newGroupApplicationIMAPAuthTestClient(
	t *testing.T,
	greeting string,
	serve func(*bufio.Reader, net.Conn) error,
) (*imapclient.Client, <-chan error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	result := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		if _, err := io.WriteString(serverConn, greeting+"\r\n"); err != nil {
			result <- err
			return
		}
		result <- serve(bufio.NewReader(serverConn), serverConn)
	}()
	client := imapclient.New(clientConn, nil)
	t.Cleanup(func() { _ = client.Close() })
	return client, result
}

func readGroupApplicationIMAPAuthTestLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), err
}

func requireGroupApplicationIMAPAuthTestServer(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("test IMAP server did not finish")
	}
}

func expectNoGroupApplicationIMAPAuthFallback(reader *bufio.Reader, conn net.Conn) error {
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return err
	}
	line, err := readGroupApplicationIMAPAuthTestLine(reader)
	if err == nil {
		return fmt.Errorf("unexpected authentication fallback: %s", line)
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return nil
	}
	return err
}

func TestAuthenticateGroupApplicationIMAPClientPrefersSASLPlain(t *testing.T) {
	for _, test := range []struct {
		name   string
		saslIR bool
	}{
		{name: "continuation"},
		{name: "initial_response", saslIR: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := "IMAP4rev1 AUTH=PLAIN"
			if test.saslIR {
				capabilities += " SASL-IR"
			}
			client, serverResult := newGroupApplicationIMAPAuthTestClient(
				t,
				"* OK [CAPABILITY "+capabilities+"] ready",
				func(reader *bufio.Reader, conn net.Conn) error {
					line, err := readGroupApplicationIMAPAuthTestLine(reader)
					if err != nil {
						return err
					}
					fields := strings.Fields(line)
					if len(fields) < 3 || fields[1] != "AUTHENTICATE" || fields[2] != "PLAIN" {
						return fmt.Errorf("unexpected authentication command: %s", line)
					}
					var encoded string
					if test.saslIR {
						if len(fields) != 4 {
							return fmt.Errorf("missing SASL initial response: %s", line)
						}
						encoded = fields[3]
					} else {
						if len(fields) != 3 {
							return fmt.Errorf("unexpected SASL initial response: %s", line)
						}
						if _, err := io.WriteString(conn, "+ \r\n"); err != nil {
							return err
						}
						encoded, err = readGroupApplicationIMAPAuthTestLine(reader)
						if err != nil {
							return err
						}
					}
					response, err := base64.StdEncoding.DecodeString(encoded)
					if err != nil {
						return err
					}
					if string(response) != "\x00inbox@example.com\x00secret" {
						return errors.New("unexpected SASL PLAIN credentials")
					}
					_, err = fmt.Fprintf(conn, "%s OK authenticated\r\n", fields[0])
					return err
				},
			)

			require.NoError(t, authenticateGroupApplicationIMAPClient(client, "inbox@example.com", "secret"))
			requireGroupApplicationIMAPAuthTestServer(t, serverResult)
		})
	}
}

func TestAuthenticateGroupApplicationIMAPClientFallsBackToLoginWhenPlainIsUnavailable(t *testing.T) {
	client, serverResult := newGroupApplicationIMAPAuthTestClient(
		t,
		"* OK [CAPABILITY IMAP4rev1] ready",
		func(reader *bufio.Reader, conn net.Conn) error {
			line, err := readGroupApplicationIMAPAuthTestLine(reader)
			if err != nil {
				return err
			}
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[1] != "LOGIN" || fields[2] != `"inbox@example.com"` || fields[3] != `"secret"` {
				return fmt.Errorf("unexpected authentication command: %s", line)
			}
			_, err = fmt.Fprintf(conn, "%s OK authenticated\r\n", fields[0])
			return err
		},
	)

	require.NoError(t, authenticateGroupApplicationIMAPClient(client, "inbox@example.com", "secret"))
	requireGroupApplicationIMAPAuthTestServer(t, serverResult)
}

func TestAuthenticateGroupApplicationIMAPClientDoesNotFallbackAfterPlainFailure(t *testing.T) {
	client, serverResult := newGroupApplicationIMAPAuthTestClient(
		t,
		"* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN] ready",
		func(reader *bufio.Reader, conn net.Conn) error {
			line, err := readGroupApplicationIMAPAuthTestLine(reader)
			if err != nil {
				return err
			}
			fields := strings.Fields(line)
			if len(fields) != 3 || fields[1] != "AUTHENTICATE" || fields[2] != "PLAIN" {
				return fmt.Errorf("unexpected authentication command: %s", line)
			}
			if _, err := io.WriteString(conn, "+ \r\n"); err != nil {
				return err
			}
			if _, err := readGroupApplicationIMAPAuthTestLine(reader); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(conn, "%s NO authentication failed\r\n", fields[0]); err != nil {
				return err
			}
			return expectNoGroupApplicationIMAPAuthFallback(reader, conn)
		},
	)

	require.Error(t, authenticateGroupApplicationIMAPClient(client, "inbox@example.com", "secret"))
	requireGroupApplicationIMAPAuthTestServer(t, serverResult)
}

func TestAuthenticateGroupApplicationIMAPClientDoesNotLoginWhenCapabilitiesFail(t *testing.T) {
	client, serverResult := newGroupApplicationIMAPAuthTestClient(
		t,
		"* OK ready",
		func(reader *bufio.Reader, conn net.Conn) error {
			line, err := readGroupApplicationIMAPAuthTestLine(reader)
			if err != nil {
				return err
			}
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[1] != "CAPABILITY" {
				return fmt.Errorf("unexpected command: %s", line)
			}
			if _, err := fmt.Fprintf(conn, "%s BAD unsupported\r\n", fields[0]); err != nil {
				return err
			}
			return expectNoGroupApplicationIMAPAuthFallback(reader, conn)
		},
	)

	err := authenticateGroupApplicationIMAPClient(client, "inbox@example.com", "secret")
	require.ErrorContains(t, err, "capabilities")
	requireGroupApplicationIMAPAuthTestServer(t, serverResult)
}

func TestOpenGroupApplicationIMAPClientCancelsStalledTLSConnection(t *testing.T) {
	for _, tlsMode := range []string{"implicit", "starttls"} {
		t.Run(tlsMode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })

			accepted := make(chan struct{})
			connectionClosed := make(chan struct{})
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer conn.Close()
				close(accepted)
				_, _ = io.Copy(io.Discard, conn)
				close(connectionClosed)
			}()

			host, portValue, err := net.SplitHostPort(listener.Addr().String())
			require.NoError(t, err)
			port, err := strconv.Atoi(portValue)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			startedAt := time.Now()
			client, err := openGroupApplicationIMAPClient(ctx, &GroupApplicationIMAPConfig{
				Host: host, Port: port, Username: "inbox@example.com", Password: "secret", TLSMode: tlsMode,
			})
			elapsed := time.Since(startedAt)
			if client != nil {
				_ = client.Close()
			}

			require.Nil(t, client)
			require.Error(t, err)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, elapsed, time.Second)
			select {
			case <-accepted:
			case <-time.After(time.Second):
				t.Fatal("test IMAP server never accepted the connection")
			}
			select {
			case <-connectionClosed:
			case <-time.After(time.Second):
				t.Fatal("context cancellation did not close the IMAP connection")
			}
		})
	}
}

func TestGroupApplicationWorkerHealthExposesMailClaimFailure(t *testing.T) {
	claimCalled := make(chan struct{})
	claimErr := errors.New("mail claim failed")
	repo := &groupApplicationWorkerRepoStub{claimErr: claimErr, claimCalled: claimCalled}
	settings := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	worker := newGroupApplicationWorker(repo, svc, &groupApplicationEmailSenderStub{})
	worker.imapPoller = func(ctx context.Context, _ *GroupApplicationIMAPConfig) error {
		<-ctx.Done()
		return ctx.Err()
	}
	worker.Start()
	t.Cleanup(worker.Stop)

	select {
	case <-claimCalled:
	case <-time.After(time.Second):
		t.Fatal("mail outbox was not checked")
	}
	require.Eventually(t, func() bool {
		health := worker.Health()
		return health.MailFailures > 0 && !health.LastMailCheckAt.IsZero() && strings.Contains(health.LastMailError, claimErr.Error())
	}, time.Second, 5*time.Millisecond)
}

func TestGroupApplicationWorkerPausesOutboxWhenWorkflowDisabled(t *testing.T) {
	repo := &groupApplicationRepositoryStub{jobs: []GroupApplicationMailJob{{
		ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
		Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
		MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
	}}}
	settings := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(repo, settings, groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{repo: repo, service: svc, email: &groupApplicationEmailSenderStub{}, workerID: "test-worker"}
	worker.lastMailError.Store("previous transport failure")

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Zero(t, repo.claimCalls)
	require.False(t, repo.retried)
	require.Empty(t, worker.Health().LastMailError)
}

func TestGroupApplicationSavePolicyRejectsMissingTemplatePayload(t *testing.T) {
	repo := &groupApplicationRepositoryStub{}
	svc := NewGroupApplicationService(repo, nil, nil)

	_, err := svc.SavePolicy(context.Background(), &GroupApplicationPolicy{GroupID: 7}, nil, 1)
	require.Error(t, err)
	require.Equal(t, "INVALID_GROUP_APPLICATION_TEMPLATES", infraerrors.Reason(err))
	require.Nil(t, repo.savedPolicy)

	policy := &GroupApplicationPolicy{GroupID: 7, Templates: DefaultGroupApplicationTemplates()}
	saved, err := svc.SavePolicy(context.Background(), policy, nil, 1)
	require.NoError(t, err)
	require.Same(t, policy, saved)
	require.Same(t, policy, repo.savedPolicy)
}

func TestGroupApplicationWorkerRefreshConfigurationUpdatesHealthImmediately(t *testing.T) {
	settings := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(&groupApplicationRepositoryStub{}, settings, groupApplicationEncryptorStub{})
	worker := &GroupApplicationWorker{service: svc}
	worker.configError.Store("")

	worker.RefreshConfiguration(context.Background())
	require.True(t, worker.Health().WorkflowEnabled)
	require.Empty(t, worker.Health().ConfigurationError)

	settings.values[SettingKeyGroupApplicationEmail] = "not-json"
	worker.RefreshConfiguration(context.Background())
	require.False(t, worker.Health().WorkflowEnabled)
	require.NotEmpty(t, worker.Health().ConfigurationError)
}

func TestGroupApplicationWorkerRefreshConfigurationWakesIMAPPoll(t *testing.T) {
	firstPoll := make(chan struct{})
	secondPoll := make(chan struct{})
	releaseFirstPoll := make(chan struct{})
	var pollMu sync.Mutex
	pollCalls := 0
	repo := &groupApplicationWorkerRepoStub{}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	worker := newGroupApplicationWorker(repo, service, &groupApplicationEmailSenderStub{})
	worker.imapPoller = func(ctx context.Context, _ *GroupApplicationIMAPConfig) error {
		pollMu.Lock()
		pollCalls++
		call := pollCalls
		pollMu.Unlock()
		switch call {
		case 1:
			close(firstPoll)
			select {
			case <-releaseFirstPoll:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case 2:
			close(secondPoll)
		}
		return nil
	}
	worker.Start()
	t.Cleanup(worker.Stop)

	select {
	case <-firstPoll:
	case <-time.After(time.Second):
		t.Fatal("initial IMAP poll did not start")
	}
	worker.lastIMAPError.Store("old IMAP failure")
	worker.lastMailError.Store("old SMTP failure")
	worker.RefreshConfiguration(context.Background())
	close(releaseFirstPoll)
	select {
	case <-secondPoll:
	case <-time.After(time.Second):
		t.Fatal("configuration refresh did not wake the IMAP loop")
	}
	require.Empty(t, worker.Health().LastIMAPError)
	require.Empty(t, worker.Health().LastMailError)
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
	worker.lastMailError.Store("previous transport failure")

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Equal(t, 1, repo.claimCalls)
	require.Equal(t, 1, repo.claimLimit)
	require.Equal(t, 1, repo.validateCalls)
	require.Equal(t, 1, repo.markSentCalls)
	require.Equal(t, 1, sender.sends)
	require.Equal(t, "smtp.example.com", sender.config.Host)
	require.Equal(t, "smtp-secret", sender.config.Password)
	require.Equal(t, "starttls", sender.config.TLSMode)
	require.Equal(t, "reply@example.com", sender.options.ReplyTo)
	require.False(t, repo.retried)
	require.Empty(t, worker.Health().LastMailError)
}

func TestGroupApplicationWorkerDoesNotClearMailErrorOnEmptyBatch(t *testing.T) {
	repo := &groupApplicationRepositoryStub{}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	worker := newGroupApplicationWorker(repo, service, &groupApplicationEmailSenderStub{})
	worker.lastMailError.Store("SMTP remains unavailable")

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Equal(t, "SMTP remains unavailable", worker.Health().LastMailError)
	require.Zero(t, repo.validateCalls)
}

func TestGroupApplicationWorkerSkipsCancelledClaimWithoutSendingOrRetrying(t *testing.T) {
	repo := &groupApplicationRepositoryStub{
		jobs: []GroupApplicationMailJob{{
			ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
			Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
			MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
		}},
		validateErr: ErrGroupApplicationState,
	}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	sender := &groupApplicationEmailSenderStub{}
	worker := newGroupApplicationWorker(repo, service, sender)
	worker.lastMailError.Store("previous transport failure")

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Equal(t, 1, repo.validateCalls)
	require.Zero(t, sender.sends)
	require.Zero(t, repo.markSentCalls)
	require.False(t, repo.retried)
	require.Zero(t, worker.mailFailures.Load())
	require.Equal(t, "previous transport failure", worker.Health().LastMailError)
}

func TestGroupApplicationWorkerReturnsClaimValidationInfrastructureErrorWithoutRetry(t *testing.T) {
	validationErr := errors.New("database unavailable while validating claim")
	repo := &groupApplicationRepositoryStub{
		jobs: []GroupApplicationMailJob{{
			ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
			Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
			MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
		}},
		validateErr: validationErr,
	}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	sender := &groupApplicationEmailSenderStub{}
	worker := newGroupApplicationWorker(repo, service, sender)

	err := worker.processMailBatch(context.Background())
	require.ErrorIs(t, err, validationErr)
	require.Equal(t, 1, repo.validateCalls)
	require.Zero(t, sender.sends)
	require.Zero(t, repo.markSentCalls)
	require.False(t, repo.retried)
	require.Zero(t, worker.mailFailures.Load())
}

func TestGroupApplicationWorkerRetriesOnlyActualSenderFailure(t *testing.T) {
	sendErr := errors.New("SMTP delivery failed")
	repo := &groupApplicationRepositoryStub{jobs: []GroupApplicationMailJob{{
		ID: 10, ApplicationID: 1, Kind: GroupApplicationMailApproval,
		Recipient: "applicant@example.com", Subject: "approved", HTMLBody: "body",
		MessageID: "<approval@sub2api.local>", RequiredStatus: GroupApplicationStatusAwaitingReply,
	}}}
	service := NewGroupApplicationService(repo, configuredGroupApplicationSettings(t), groupApplicationEncryptorStub{})
	sender := &groupApplicationEmailSenderStub{err: sendErr}
	worker := newGroupApplicationWorker(repo, service, sender)

	require.NoError(t, worker.processMailBatch(context.Background()))
	require.Equal(t, 1, repo.validateCalls)
	require.Equal(t, 1, sender.sends)
	require.True(t, repo.retried)
	require.Contains(t, repo.retriedLastError, sendErr.Error())
	require.Equal(t, uint64(1), worker.mailFailures.Load())
	require.Contains(t, worker.Health().LastMailError, sendErr.Error())
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

func TestGroupApplicationWorkflowEntryMutationsAreBlockedWhenDisabled(t *testing.T) {
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
