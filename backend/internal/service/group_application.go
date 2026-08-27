package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const (
	GroupApplicationStatusPending       = "pending"
	GroupApplicationStatusAwaitingReply = "awaiting_reply"
	GroupApplicationStatusCompleted     = "completed"
	GroupApplicationStatusRejected      = "rejected"
	GroupApplicationStatusRevoked       = "revoked"

	GroupApplicationMailApproval        = "approval"
	GroupApplicationMailCompletion      = "completion"
	GroupApplicationMailManualRejection = "manual_rejection"
	GroupApplicationMailReplyMismatch   = "reply_mismatch"
	GroupApplicationMailRevocation      = "revocation"

	GroupApplicationMaxAttachmentBytes    = 10 << 20
	GroupApplicationMaxStoredReplyRunes   = 8000
	GroupApplicationMaxStoredSubjectRunes = 500
	SettingKeyGroupApplicationEmail       = "group_application_email_config"
	SettingKeyGroupApplicationIMAP        = "group_application_imap_config" // legacy read-only fallback

	GroupApplicationCommunicationOutbound = "outbound"
	GroupApplicationCommunicationInbound  = "inbound"
)

var (
	ErrGroupApplicationUnavailable = infraerrors.BadRequest("GROUP_APPLICATION_UNAVAILABLE", "group is not available for application")
	ErrGroupApplicationDisabled    = infraerrors.Conflict("GROUP_APPLICATION_DISABLED", "group application workflow is disabled")
	ErrGroupApplicationConflict    = infraerrors.Conflict("GROUP_APPLICATION_CONFLICT", "an active or completed application already exists for this group")
	ErrGroupApplicationNotFound    = infraerrors.NotFound("GROUP_APPLICATION_NOT_FOUND", "group application not found")
	ErrGroupApplicationState       = infraerrors.Conflict("GROUP_APPLICATION_STATE_CONFLICT", "group application status has changed")
	ErrGroupApplicationEmail       = infraerrors.BadRequest("GROUP_APPLICATION_EMAIL_INVALID", "valid standalone group application email configuration is required")
)

type GroupApplicationLocalizedTemplate struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type GroupApplicationTemplateSet map[string]map[string]GroupApplicationLocalizedTemplate

type GroupApplicationPolicy struct {
	GroupID          int64                       `json:"group_id"`
	GroupName        string                      `json:"group_name"`
	Enabled          bool                        `json:"enabled"`
	ReplyPhrase      string                      `json:"reply_phrase,omitempty"`
	Templates        GroupApplicationTemplateSet `json:"templates"`
	AttachmentID     *int64                      `json:"attachment_id,omitempty"`
	AttachmentName   string                      `json:"attachment_name,omitempty"`
	AttachmentSize   int64                       `json:"attachment_size,omitempty"`
	AttachmentSHA256 string                      `json:"attachment_sha256,omitempty"`
	CreatedAt        time.Time                   `json:"created_at"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}

type GroupApplicationAttachment struct {
	ID          int64     `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	ByteSize    int64     `json:"byte_size"`
	SHA256      string    `json:"sha256"`
	Data        []byte    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
}

type GroupApplication struct {
	ID                  int64                        `json:"id"`
	UserID              int64                        `json:"user_id"`
	UserEmail           string                       `json:"user_email,omitempty"`
	GroupID             int64                        `json:"group_id"`
	GroupName           string                       `json:"group_name"`
	ContactEmail        string                       `json:"contact_email"`
	Reason              string                       `json:"reason"`
	Locale              string                       `json:"locale"`
	Status              string                       `json:"status"`
	ReplyPhraseSnapshot string                       `json:"-"`
	TemplatesSnapshot   GroupApplicationTemplateSet  `json:"-"`
	AttachmentID        int64                        `json:"attachment_id"`
	AttachmentName      string                       `json:"attachment_name,omitempty"`
	ReviewedBy          *int64                       `json:"reviewed_by,omitempty"`
	ReviewedAt          *time.Time                   `json:"reviewed_at,omitempty"`
	DecisionReason      string                       `json:"decision_reason,omitempty"`
	CompletedAt         *time.Time                   `json:"completed_at,omitempty"`
	RevokedBy           *int64                       `json:"revoked_by,omitempty"`
	RevokedAt           *time.Time                   `json:"revoked_at,omitempty"`
	LastEmailKind       string                       `json:"last_email_kind,omitempty"`
	LastEmailStatus     string                       `json:"last_email_status,omitempty"`
	LastEmailError      string                       `json:"last_email_error,omitempty"`
	CreatedAt           time.Time                    `json:"created_at"`
	UpdatedAt           time.Time                    `json:"updated_at"`
	Mails               []GroupApplicationMailStatus `json:"mails,omitempty"`
}

type GroupApplicationMailStatus struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	MessageID string     `json:"message_id"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"last_error,omitempty"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type GroupApplicationCommunication struct {
	ID                 int64      `json:"id"`
	ApplicationID      int64      `json:"application_id"`
	Direction          string     `json:"direction"`
	Kind               string     `json:"kind,omitempty"`
	Result             string     `json:"result,omitempty"`
	FromAddress        string     `json:"from_address,omitempty"`
	ToAddress          string     `json:"to_address,omitempty"`
	Subject            string     `json:"subject,omitempty"`
	HTMLBody           string     `json:"html_body,omitempty"`
	TextBody           string     `json:"text_body,omitempty"`
	ContentUnavailable bool       `json:"content_unavailable,omitempty"`
	ContentTruncated   bool       `json:"content_truncated,omitempty"`
	MessageID          string     `json:"message_id,omitempty"`
	InReplyTo          string     `json:"in_reply_to,omitempty"`
	References         string     `json:"references,omitempty"`
	ReplySHA256        string     `json:"reply_sha256,omitempty"`
	AttachmentID       *int64     `json:"attachment_id,omitempty"`
	AttachmentName     string     `json:"attachment_name,omitempty"`
	AttachmentSize     int64      `json:"attachment_size,omitempty"`
	Status             string     `json:"status,omitempty"`
	Attempts           int        `json:"attempts,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	SentAt             *time.Time `json:"sent_at,omitempty"`
	OccurredAt         time.Time  `json:"occurred_at"`
	EncryptedContent   string     `json:"-"`
}

type GroupApplicationOption struct {
	GroupID          int64  `json:"group_id"`
	GroupName        string `json:"group_name"`
	Description      string `json:"description,omitempty"`
	HasActive        bool   `json:"has_active"`
	AlreadyCompleted bool   `json:"already_completed"`
}

type GroupApplicationListFilter struct {
	Status  string
	UserID  int64
	GroupID int64
	Search  string
	Offset  int
	Limit   int
}

type GroupApplicationListResult struct {
	Items []*GroupApplication `json:"items"`
	Total int64               `json:"total"`
}

type GroupApplicationMailJob struct {
	ID             int64
	ApplicationID  int64
	Kind           string
	Recipient      string
	Subject        string
	HTMLBody       string
	AttachmentID   *int64
	MessageID      string
	RequiredStatus string
	Attempts       int
	Attachment     *GroupApplicationAttachment
}

type GroupApplicationApprovalMatch struct {
	Application *GroupApplication
	MessageID   string
}

type GroupApplicationReceipt struct {
	MailboxFingerprint string
	UIDValidity        uint32
	UID                uint32
	MessageID          string
	FromAddress        string
	InReplyTo          string
	References         string
	ApplicationID      *int64
	Result             string
	ReplySHA256        string
	EncryptedContent   string
	ContentTruncated   bool
}

type GroupApplicationRepository interface {
	ListOptions(ctx context.Context, userID int64) ([]GroupApplicationOption, error)
	GetPolicy(ctx context.Context, groupID int64) (*GroupApplicationPolicy, error)
	ListPolicies(ctx context.Context) ([]*GroupApplicationPolicy, error)
	SavePolicy(ctx context.Context, policy *GroupApplicationPolicy, attachment *GroupApplicationAttachment, adminID int64) (*GroupApplicationPolicy, error)
	GetAttachment(ctx context.Context, id int64) (*GroupApplicationAttachment, error)
	CreateApplication(ctx context.Context, application *GroupApplication) (*GroupApplication, error)
	ListUserApplications(ctx context.Context, userID int64) ([]*GroupApplication, error)
	GetUserApplication(ctx context.Context, userID, applicationID int64) (*GroupApplication, error)
	ListApplications(ctx context.Context, filter GroupApplicationListFilter) (*GroupApplicationListResult, error)
	GetApplication(ctx context.Context, applicationID int64) (*GroupApplication, error)
	ListApplicationMails(ctx context.Context, applicationID int64) ([]GroupApplicationMailStatus, error)
	ListApplicationCommunications(ctx context.Context, applicationID int64) ([]GroupApplicationCommunication, error)
	Approve(ctx context.Context, applicationID, adminID int64, mail GroupApplicationMailJob) (*GroupApplication, error)
	Reject(ctx context.Context, applicationID, adminID int64, reason string, mail GroupApplicationMailJob) (*GroupApplication, error)
	CompleteFromReply(ctx context.Context, applicationID int64, mail GroupApplicationMailJob) (*GroupApplication, error)
	RejectReplyMismatch(ctx context.Context, applicationID int64, mail GroupApplicationMailJob) (*GroupApplication, error)
	Revoke(ctx context.Context, applicationID, adminID int64, reason string, mail GroupApplicationMailJob) (*GroupApplication, error)
	EnqueueMail(ctx context.Context, applicationID int64, mail GroupApplicationMailJob) error
	RetryMail(ctx context.Context, applicationID, outboxID int64) error
	ClaimMail(ctx context.Context, workerID string, limit int, lease time.Duration) ([]GroupApplicationMailJob, error)
	MarkMailSent(ctx context.Context, id int64, workerID string) error
	RetryClaimedMail(ctx context.Context, id int64, workerID string, retryAt time.Time, terminal bool, lastError string) error
	FindApprovalByMessageIDs(ctx context.Context, messageIDs []string) (*GroupApplicationApprovalMatch, error)
	MaxProcessedUID(ctx context.Context, fingerprint string, uidValidity uint32) (uint32, bool, error)
	StoreReceipt(ctx context.Context, receipt GroupApplicationReceipt) (bool, error)
}

type GroupApplicationService struct {
	repo        GroupApplicationRepository
	settingRepo SettingRepository
	encryptor   SecretEncryptor
}

func NewGroupApplicationService(repo GroupApplicationRepository, settingRepo SettingRepository, encryptor SecretEncryptor) *GroupApplicationService {
	return &GroupApplicationService{repo: repo, settingRepo: settingRepo, encryptor: encryptor}
}

func DefaultGroupApplicationTemplates() GroupApplicationTemplateSet {
	return GroupApplicationTemplateSet{
		GroupApplicationMailApproval: {
			"zh": {Subject: "{{site_name}} - {{group_name}} 分组申请已批准", HTML: "<p>您的申请已被批准，但在正式为您开放访问权限前，您需仔细阅读附件中的协议，并直接回复本邮件“<strong>{{reply_phrase}}</strong>”。回复正文不得包含其他内容或签名。</p>"},
			"en": {Subject: "{{site_name}} - {{group_name}} application approved", HTML: "<p>Your application has been approved. Before access is opened, read the attached agreement and reply to this email with exactly <strong>{{reply_phrase}}</strong>. Do not include any other text or signature.</p>"},
		},
		GroupApplicationMailCompletion: {
			"zh": {Subject: "{{site_name}} - {{group_name}} 访问权限已开放", HTML: "<p>您对 {{group_name}} 的申请已完成，访问权限现已开放。请前往 API 密钥页将需要使用的密钥切换到该分组。</p>"},
			"en": {Subject: "{{site_name}} - {{group_name}} access enabled", HTML: "<p>Your application for {{group_name}} is complete and access is now enabled. Open the API Keys page to switch the keys that should use this group.</p>"},
		},
		GroupApplicationMailManualRejection: {
			"zh": {Subject: "{{site_name}} - {{group_name}} 分组申请未通过", HTML: "<p>您的 {{group_name}} 分组申请未通过。拒绝理由：{{decision_reason}}。</p>"},
			"en": {Subject: "{{site_name}} - {{group_name}} application declined", HTML: "<p>Your application for {{group_name}} was declined. Reason: {{decision_reason}}.</p>"},
		},
		GroupApplicationMailReplyMismatch: {
			"zh": {Subject: "{{site_name}} - {{group_name}} 邮件确认不匹配", HTML: "<p>您的邮件回复与要求的确认内容不完全一致，本次申请已自动拒绝。您可以重新提交申请。</p>"},
			"en": {Subject: "{{site_name}} - {{group_name}} confirmation did not match", HTML: "<p>Your reply did not exactly match the required confirmation. This application was automatically declined and you may submit a new application.</p>"},
		},
		GroupApplicationMailRevocation: {
			"zh": {Subject: "{{site_name}} - {{group_name}} 访问权限已撤销", HTML: "<p>您的 {{group_name}} 分组访问权限已被撤销。撤销理由：{{decision_reason}}。已绑定该分组的 API 密钥将无法继续使用。</p>"},
			"en": {Subject: "{{site_name}} - {{group_name}} access revoked", HTML: "<p>Your access to {{group_name}} was revoked. Reason: {{decision_reason}}. API keys already bound to this group can no longer be used.</p>"},
		},
	}
}

var groupApplicationTemplateToken = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)
var groupApplicationTemplateAnyToken = regexp.MustCompile(`\{\{[^{}]*\}\}`)

var groupApplicationAllowedTokens = map[string]struct{}{
	"site_name": {}, "recipient": {}, "application_id": {}, "group_name": {},
	"application_reason": {}, "reply_phrase": {}, "attachment_name": {},
	"decision_reason": {}, "submitted_at": {}, "reviewed_at": {},
}

func NormalizeGroupApplicationTemplates(in GroupApplicationTemplateSet) (GroupApplicationTemplateSet, error) {
	defaults := DefaultGroupApplicationTemplates()
	out := make(GroupApplicationTemplateSet, len(defaults))
	for _, kind := range []string{GroupApplicationMailApproval, GroupApplicationMailCompletion, GroupApplicationMailManualRejection, GroupApplicationMailReplyMismatch, GroupApplicationMailRevocation} {
		out[kind] = map[string]GroupApplicationLocalizedTemplate{}
		for _, locale := range []string{"zh", "en"} {
			value := defaults[kind][locale]
			if byLocale := in[kind]; byLocale != nil {
				candidate := byLocale[locale]
				if strings.TrimSpace(candidate.Subject) != "" {
					value.Subject = strings.TrimSpace(candidate.Subject)
				}
				if strings.TrimSpace(candidate.HTML) != "" {
					value.HTML = strings.TrimSpace(candidate.HTML)
				}
			}
			if err := validateGroupApplicationTemplate(value); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", kind, locale, err)
			}
			out[kind][locale] = value
		}
	}
	if !groupApplicationTemplateHasToken(out[GroupApplicationMailApproval]["zh"], "reply_phrase") ||
		!groupApplicationTemplateHasToken(out[GroupApplicationMailApproval]["en"], "reply_phrase") {
		return nil, errors.New("approval templates must include {{reply_phrase}}")
	}
	for _, kind := range []string{GroupApplicationMailManualRejection, GroupApplicationMailRevocation} {
		for _, locale := range []string{"zh", "en"} {
			value := out[kind][locale]
			if !groupApplicationTemplateHasToken(value, "decision_reason") {
				return nil, fmt.Errorf("%s.%s must include {{decision_reason}}", kind, locale)
			}
		}
	}
	return out, nil
}

func groupApplicationTemplateHasToken(value GroupApplicationLocalizedTemplate, name string) bool {
	for _, match := range groupApplicationTemplateToken.FindAllStringSubmatch(value.Subject+value.HTML, -1) {
		if len(match) == 2 && match[1] == name {
			return true
		}
	}
	return false
}

func validateGroupApplicationTemplate(value GroupApplicationLocalizedTemplate) error {
	if len(value.Subject) > 300 || len(value.HTML) > 100000 {
		return errors.New("template is too large")
	}
	for _, source := range []string{value.Subject, value.HTML} {
		for _, token := range groupApplicationTemplateAnyToken.FindAllString(source, -1) {
			match := groupApplicationTemplateToken.FindStringSubmatch(token)
			if len(match) != 2 {
				return fmt.Errorf("invalid placeholder %q", token)
			}
			if _, ok := groupApplicationAllowedTokens[match[1]]; !ok {
				return fmt.Errorf("unknown placeholder %q", match[1])
			}
		}
	}
	return nil
}

func RenderGroupApplicationTemplate(value GroupApplicationLocalizedTemplate, variables map[string]string) (string, string, error) {
	replace := func(source string) (string, error) {
		var renderErr error
		result := groupApplicationTemplateToken.ReplaceAllStringFunc(source, func(token string) string {
			match := groupApplicationTemplateToken.FindStringSubmatch(token)
			if len(match) != 2 {
				return token
			}
			if _, ok := groupApplicationAllowedTokens[match[1]]; !ok {
				renderErr = fmt.Errorf("unknown placeholder %q", match[1])
				return token
			}
			return html.EscapeString(variables[match[1]])
		})
		return result, renderErr
	}
	subject, err := replace(value.Subject)
	if err != nil {
		return "", "", err
	}
	body, err := replace(value.HTML)
	return subject, body, err
}

func NormalizeGroupApplicationLocale(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en") {
		return "en"
	}
	return "zh"
}

func NormalizeGroupApplicationEmail(value string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(address.Address) == "" || len(address.Address) > 320 {
		return "", infraerrors.BadRequest("INVALID_CONTACT_EMAIL", "invalid contact email")
	}
	return strings.ToLower(strings.TrimSpace(address.Address)), nil
}

func NormalizeGroupApplicationReply(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Trim(value, "\n")
}

func GroupApplicationReplyDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newGroupApplicationMessageID(kind string, applicationID int64) string {
	return fmt.Sprintf("<group-application-%d-%s-%s@sub2api.local>", applicationID, kind, uuid.NewString())
}

func messageIDCandidates(inReplyTo, references string) []string {
	candidates := make(map[string]struct{})
	for _, source := range []string{inReplyTo, references} {
		for _, field := range strings.Fields(source) {
			field = strings.TrimSpace(strings.Trim(field, ",;"))
			if strings.HasPrefix(field, "<") && strings.HasSuffix(field, ">") {
				candidates[field] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(candidates))
	for value := range candidates {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
