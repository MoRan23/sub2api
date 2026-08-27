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

	GroupApplicationReplyStatusAwaitingReply = "awaiting_reply"
	GroupApplicationReplyStatusCompleted     = "completed"

	GroupApplicationMaxAttachmentBytes        = 10 << 20
	GroupApplicationMaxStoredReplyRunes       = 8000
	GroupApplicationMaxStoredSubjectRunes     = 500
	groupApplicationMaxMessageIDCandidates    = 32
	groupApplicationMaxMessageIDTokenBytes    = 255
	groupApplicationMaxStoredMessageIDRunes   = 255
	groupApplicationMaxStoredFromAddressRunes = 320
	groupApplicationMaxStoredReferencesRunes  = 8192
	SettingKeyGroupApplicationEmail           = "group_application_email_config"
	SettingKeyGroupApplicationIMAP            = "group_application_imap_config" // legacy read-only fallback

	GroupApplicationCommunicationOutbound = "outbound"
	GroupApplicationCommunicationInbound  = "inbound"
)

var (
	ErrGroupApplicationUnavailable    = infraerrors.BadRequest("GROUP_APPLICATION_UNAVAILABLE", "group is not available for application")
	ErrGroupApplicationDisabled       = infraerrors.Conflict("GROUP_APPLICATION_DISABLED", "group application workflow is disabled")
	ErrGroupApplicationConflict       = infraerrors.Conflict("GROUP_APPLICATION_CONFLICT", "an active or completed application already exists for this group")
	ErrGroupApplicationNotFound       = infraerrors.NotFound("GROUP_APPLICATION_NOT_FOUND", "group application not found")
	ErrGroupApplicationState          = infraerrors.Conflict("GROUP_APPLICATION_STATE_CONFLICT", "group application status has changed")
	ErrGroupApplicationEmail          = infraerrors.BadRequest("GROUP_APPLICATION_EMAIL_INVALID", "valid standalone group application email configuration is required")
	ErrGroupApplicationReplyAmbiguous = errors.New("group application reply references multiple active applications")
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
	ID             int64      `json:"id"`
	Kind           string     `json:"kind"`
	MessageID      string     `json:"message_id"`
	Status         string     `json:"status"`
	RequiredStatus string     `json:"-"`
	ReplyStatus    string     `json:"reply_status,omitempty"`
	DeliveryActive bool       `json:"delivery_active,omitempty"`
	Retryable      bool       `json:"retryable,omitempty"`
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"last_error,omitempty"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
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
	RequiredStatus     string     `json:"-"`
	ReplyStatus        string     `json:"reply_status,omitempty"`
	DeliveryActive     bool       `json:"delivery_active,omitempty"`
	Retryable          bool       `json:"retryable,omitempty"`
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

func legacyDefaultGroupApplicationTemplates() GroupApplicationTemplateSet {
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

func groupApplicationEmailCard(accent, eyebrow, title, content, footer string) string {
	const document = `<!doctype html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>[[TITLE]]</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;color:#17202a;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
  <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:collapse;background:#f3f4f6;">
    <tr>
      <td align="center" style="padding:32px 16px;">
        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;max-width:640px;border-collapse:separate;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;box-shadow:0 10px 28px rgba(15,23,42,0.08);">
          <tr><td style="height:8px;background:[[ACCENT]];font-size:0;line-height:0;">&nbsp;</td></tr>
          <tr>
            <td style="padding:36px 40px 28px;">
              <p style="margin:0 0 10px;color:[[ACCENT]];font-size:12px;font-weight:700;letter-spacing:0;text-transform:uppercase;">[[EYEBROW]]</p>
              <h1 style="margin:0 0 24px;color:#111827;font-size:28px;line-height:1.3;font-weight:700;">[[TITLE]]</h1>
              [[CONTENT]]
            </td>
          </tr>
          <tr>
            <td style="padding:18px 40px;background:#f9fafb;border-top:1px solid #e5e7eb;">
              <p style="margin:0;color:#6b7280;font-size:12px;line-height:1.7;">[[FOOTER]]</p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`
	return strings.NewReplacer(
		"[[ACCENT]]", accent,
		"[[EYEBROW]]", eyebrow,
		"[[TITLE]]", title,
		"[[CONTENT]]", content,
		"[[FOOTER]]", footer,
	).Replace(document)
}

func DefaultGroupApplicationTemplates() GroupApplicationTemplateSet {
	return GroupApplicationTemplateSet{
		GroupApplicationMailApproval: {
			"zh": {
				Subject: "{{site_name}} - {{group_name}} 分组申请已批准",
				HTML: groupApplicationEmailCard("#0f766e", "GROUP ACCESS / 分组申请", "申请已通过初审", `
              <p style="margin:0 0 14px;font-size:16px;line-height:1.8;">您好，</p>
              <p style="margin:0 0 22px;font-size:16px;line-height:1.8;">您的 <strong>{{group_name}}</strong> 分组申请已获批准。正式开放访问权限前，请完成以下确认步骤。</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#ecfdf5;border:1px solid #a7f3d0;border-radius:8px;">
                <tr><td style="padding:18px 20px;color:#065f46;font-size:14px;line-height:1.8;"><strong>1.</strong> 阅读本邮件附件 <strong>{{attachment_name}}</strong><br><strong>2.</strong> 使用当前邮箱直接回复本邮件<br><strong>3.</strong> 回复正文只能包含下方确认词，不得附加签名或其他文字</td></tr>
              </table>
              <p style="margin:24px 0 10px;color:#4b5563;font-size:13px;font-weight:600;">严格回复词</p>
              <div style="padding:16px 20px;background:#111827;color:#ffffff;border-radius:8px;font-family:ui-monospace,SFMono-Regular,Consolas,'Liberation Mono',monospace;font-size:18px;font-weight:700;line-height:1.5;text-align:center;overflow-wrap:anywhere;">{{reply_phrase}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}} · 提交时间：{{submitted_at}}</p>`, "请直接回复本邮件完成确认。本邮件由 {{site_name}} 的分组申请系统发送。"),
			},
			"en": {
				Subject: "{{site_name}} - {{group_name}} application approved",
				HTML: groupApplicationEmailCard("#0f766e", "GROUP ACCESS", "Application approved for confirmation", `
              <p style="margin:0 0 14px;font-size:16px;line-height:1.8;">Hello,</p>
              <p style="margin:0 0 22px;font-size:16px;line-height:1.8;">Your application for <strong>{{group_name}}</strong> has been approved. Complete the confirmation below before access is enabled.</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#ecfdf5;border:1px solid #a7f3d0;border-radius:8px;">
                <tr><td style="padding:18px 20px;color:#065f46;font-size:14px;line-height:1.8;"><strong>1.</strong> Read the attached agreement <strong>{{attachment_name}}</strong><br><strong>2.</strong> Reply directly from this email address<br><strong>3.</strong> Put only the confirmation phrase below in the reply body, with no signature or other text</td></tr>
              </table>
              <p style="margin:24px 0 10px;color:#4b5563;font-size:13px;font-weight:600;">Exact confirmation phrase</p>
              <div style="padding:16px 20px;background:#111827;color:#ffffff;border-radius:8px;font-family:ui-monospace,SFMono-Regular,Consolas,'Liberation Mono',monospace;font-size:18px;font-weight:700;line-height:1.5;text-align:center;overflow-wrap:anywhere;">{{reply_phrase}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}} · Submitted {{submitted_at}}</p>`, "Reply directly to this email to confirm. This message was sent by the {{site_name}} group application system."),
			},
		},
		GroupApplicationMailCompletion: {
			"zh": {
				Subject: "{{site_name}} - {{group_name}} 访问权限已开放",
				HTML: groupApplicationEmailCard("#15803d", "GROUP ACCESS / 分组申请", "访问权限已开放", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">您的邮件确认已验证通过，<strong>{{group_name}}</strong> 分组访问权限现已正式开放。</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#f0fdf4;border:1px solid #bbf7d0;border-radius:8px;">
                <tr><td style="padding:20px;color:#166534;font-size:15px;line-height:1.8;"><strong>已完成</strong><br>前往 API 密钥页，将需要使用专属能力的密钥切换到 {{group_name}} 分组。</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}}</p>`, "此邮件由 {{site_name}} 的分组申请系统自动发送。"),
			},
			"en": {
				Subject: "{{site_name}} - {{group_name}} access enabled",
				HTML: groupApplicationEmailCard("#15803d", "GROUP ACCESS", "Access is now enabled", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">Your email confirmation was verified and access to <strong>{{group_name}}</strong> is now enabled.</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#f0fdf4;border:1px solid #bbf7d0;border-radius:8px;">
                <tr><td style="padding:20px;color:#166534;font-size:15px;line-height:1.8;"><strong>Completed</strong><br>Open the API Keys page and switch the keys that need this access to the {{group_name}} group.</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}}</p>`, "This message was sent automatically by the {{site_name}} group application system."),
			},
		},
		GroupApplicationMailManualRejection: {
			"zh": {
				Subject: "{{site_name}} - {{group_name}} 分组申请未通过",
				HTML: groupApplicationEmailCard("#b91c1c", "GROUP ACCESS / 分组申请", "申请未通过", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">您提交的 <strong>{{group_name}}</strong> 分组申请未获批准。</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">审核理由</p>
              <div style="padding:18px 20px;background:#fef2f2;border:1px solid #fecaca;border-radius:8px;color:#991b1b;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}}。如条件发生变化，您可以重新提交申请。</p>`, "此邮件由 {{site_name}} 的分组申请系统自动发送。"),
			},
			"en": {
				Subject: "{{site_name}} - {{group_name}} application declined",
				HTML: groupApplicationEmailCard("#b91c1c", "GROUP ACCESS", "Application declined", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">Your application for <strong>{{group_name}}</strong> was not approved.</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">Decision reason</p>
              <div style="padding:18px 20px;background:#fef2f2;border:1px solid #fecaca;border-radius:8px;color:#991b1b;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}}. You may submit a new application if your circumstances change.</p>`, "This message was sent automatically by the {{site_name}} group application system."),
			},
		},
		GroupApplicationMailReplyMismatch: {
			"zh": {
				Subject: "{{site_name}} - {{group_name}} 邮件确认不匹配",
				HTML: groupApplicationEmailCard("#b45309", "GROUP ACCESS / 分组申请", "回复验证未通过", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">系统未能验证您对 <strong>{{group_name}}</strong> 分组申请的邮件回复。</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#fffbeb;border:1px solid #fde68a;border-radius:8px;">
                <tr><td style="padding:20px;color:#92400e;font-size:15px;line-height:1.8;">回复正文与要求的严格确认词不完全一致，本次申请已自动拒绝。常见原因包括额外签名、空格或其他文字。</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}}。您可以重新提交申请并再次完成邮件确认。</p>`, "此邮件由 {{site_name}} 的分组申请系统自动发送。"),
			},
			"en": {
				Subject: "{{site_name}} - {{group_name}} confirmation did not match",
				HTML: groupApplicationEmailCard("#b45309", "GROUP ACCESS", "Reply verification failed", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">We could not verify your email reply for the <strong>{{group_name}}</strong> group application.</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#fffbeb;border:1px solid #fde68a;border-radius:8px;">
                <tr><td style="padding:20px;color:#92400e;font-size:15px;line-height:1.8;">The reply body did not exactly match the required phrase, so this application was automatically declined. Common causes include signatures, spaces, or extra text.</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}}. You may submit a new application and complete confirmation again.</p>`, "This message was sent automatically by the {{site_name}} group application system."),
			},
		},
		GroupApplicationMailRevocation: {
			"zh": {
				Subject: "{{site_name}} - {{group_name}} 访问权限已撤销",
				HTML: groupApplicationEmailCard("#9f1239", "GROUP ACCESS / 分组申请", "访问权限已撤销", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">您的 <strong>{{group_name}}</strong> 分组访问权限已被管理员撤销。</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">撤销理由</p>
              <div style="padding:18px 20px;background:#fff1f2;border:1px solid #fecdd3;border-radius:8px;color:#9f1239;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">已绑定该分组的 API 密钥将无法继续使用。申请编号：#{{application_id}}</p>`, "此邮件由 {{site_name}} 的分组申请系统自动发送。"),
			},
			"en": {
				Subject: "{{site_name}} - {{group_name}} access revoked",
				HTML: groupApplicationEmailCard("#9f1239", "GROUP ACCESS", "Access revoked", `
              <p style="margin:0 0 18px;font-size:16px;line-height:1.8;">Your access to the <strong>{{group_name}}</strong> group has been revoked by an administrator.</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">Revocation reason</p>
              <div style="padding:18px 20px;background:#fff1f2;border:1px solid #fecdd3;border-radius:8px;color:#9f1239;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">API keys assigned to this group can no longer be used. Application #{{application_id}}</p>`, "This message was sent automatically by the {{site_name}} group application system."),
			},
		},
	}
}

var groupApplicationTemplateToken = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)
var groupApplicationTemplateAnyToken = regexp.MustCompile(`\{\{[^{}]*\}\}`)
var groupApplicationApprovalMessageIDFragment = regexp.MustCompile(`group-application-[1-9][0-9]{0,18}-approval-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}@sub2api\.local`)

var groupApplicationAllowedTokens = map[string]struct{}{
	"site_name": {}, "recipient": {}, "application_id": {}, "group_name": {},
	"application_reason": {}, "reply_phrase": {}, "attachment_name": {},
	"decision_reason": {}, "submitted_at": {}, "reviewed_at": {},
}

func NormalizeGroupApplicationTemplates(in GroupApplicationTemplateSet) (GroupApplicationTemplateSet, error) {
	defaults := DefaultGroupApplicationTemplates()
	legacyDefaults := legacyDefaultGroupApplicationTemplates()
	out := make(GroupApplicationTemplateSet, len(defaults))
	for _, kind := range []string{GroupApplicationMailApproval, GroupApplicationMailCompletion, GroupApplicationMailManualRejection, GroupApplicationMailReplyMismatch, GroupApplicationMailRevocation} {
		out[kind] = map[string]GroupApplicationLocalizedTemplate{}
		for _, locale := range []string{"zh", "en"} {
			value := defaults[kind][locale]
			if byLocale := in[kind]; byLocale != nil {
				candidate := byLocale[locale]
				legacy := legacyDefaults[kind][locale]
				if strings.TrimSpace(candidate.Subject) != "" && candidate.Subject != legacy.Subject {
					value.Subject = strings.TrimSpace(candidate.Subject)
				}
				if strings.TrimSpace(candidate.HTML) != "" && candidate.HTML != legacy.HTML {
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
		for offset := 0; offset < len(source) && len(candidates) < groupApplicationMaxMessageIDCandidates; {
			for offset < len(source) && isGroupApplicationHeaderSpace(source[offset]) {
				offset++
			}
			start := offset
			for offset < len(source) && !isGroupApplicationHeaderSpace(source[offset]) {
				offset++
			}
			if start == offset {
				continue
			}
			field := source[start:offset]
			field = strings.TrimSpace(strings.Trim(field, ",;"))
			if isSafeGroupApplicationMessageIDCandidate(field) {
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

func isGroupApplicationHeaderSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '\v', '\f':
		return true
	default:
		return false
	}
}

func isSafeGroupApplicationMessageIDCandidate(value string) bool {
	if len(value) < 3 || len(value) > groupApplicationMaxMessageIDTokenBytes || value[0] != '<' || value[len(value)-1] != '>' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func embeddedGroupApplicationApprovalMessageIDCandidates(sources ...string) []string {
	candidates := make(map[string]struct{})
	for _, source := range sources {
		for offset := 0; offset < len(source) && len(candidates) < groupApplicationMaxMessageIDCandidates; {
			location := groupApplicationApprovalMessageIDFragment.FindStringIndex(source[offset:])
			if location == nil {
				break
			}
			fragment := source[offset+location[0] : offset+location[1]]
			candidates["<"+fragment+">"] = struct{}{}
			offset += location[1]
		}
	}
	out := make([]string, 0, len(candidates))
	for value := range candidates {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func groupApplicationReceiptMetadata(value string, maxRunes int) string {
	value = strings.ReplaceAll(strings.ToValidUTF8(value, "\uFFFD"), "\x00", "")
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}
