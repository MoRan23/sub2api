package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *GroupApplicationService) ListOptions(ctx context.Context, userID int64) ([]GroupApplicationOption, error) {
	enabled, err := s.GroupApplicationWorkflowEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return []GroupApplicationOption{}, nil
	}
	return s.repo.ListOptions(ctx, userID)
}

func (s *GroupApplicationService) GroupApplicationWorkflowEnabled(ctx context.Context) (bool, error) {
	config, err := s.LoadEmailConfig(ctx, false)
	if err != nil {
		return false, err
	}
	return config.Enabled, nil
}

func (s *GroupApplicationService) requireGroupApplicationWorkflow(ctx context.Context) error {
	enabled, err := s.GroupApplicationWorkflowEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrGroupApplicationDisabled
	}
	return nil
}

func (s *GroupApplicationService) ListUserApplications(ctx context.Context, userID int64) ([]*GroupApplication, error) {
	return s.repo.ListUserApplications(ctx, userID)
}

func (s *GroupApplicationService) GetUserApplication(ctx context.Context, userID, applicationID int64) (*GroupApplication, error) {
	return s.repo.GetUserApplication(ctx, userID, applicationID)
}

func (s *GroupApplicationService) Submit(ctx context.Context, userID, groupID int64, contactEmail, reason, locale string) (*GroupApplication, error) {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return nil, err
	}
	if userID <= 0 || groupID <= 0 {
		return nil, ErrGroupApplicationUnavailable
	}
	email, err := NormalizeGroupApplicationEmail(contactEmail)
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < 5 || len([]rune(reason)) > 5000 {
		return nil, infraerrors.BadRequest("INVALID_APPLICATION_REASON", "application reason must contain 5 to 5000 characters")
	}
	return s.repo.CreateApplication(ctx, &GroupApplication{
		UserID: userID, GroupID: groupID, ContactEmail: email,
		Reason: reason, Locale: NormalizeGroupApplicationLocale(locale),
	})
}

func (s *GroupApplicationService) ListPolicies(ctx context.Context) ([]*GroupApplicationPolicy, error) {
	items, err := s.repo.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		item.Templates, err = NormalizeGroupApplicationTemplates(item.Templates)
		if err != nil {
			return nil, fmt.Errorf("normalize group application policy %d: %w", item.GroupID, err)
		}
	}
	return items, nil
}

func (s *GroupApplicationService) SavePolicy(ctx context.Context, policy *GroupApplicationPolicy, attachment *GroupApplicationAttachment, adminID int64) (*GroupApplicationPolicy, error) {
	if policy == nil || policy.GroupID <= 0 || adminID <= 0 {
		return nil, ErrGroupApplicationUnavailable
	}
	if policy.Templates == nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_TEMPLATES", "mail templates are required")
	}
	policy.ReplyPhrase = strings.TrimSpace(NormalizeGroupApplicationReply(policy.ReplyPhrase))
	if len([]rune(policy.ReplyPhrase)) > 500 || (policy.Enabled && policy.ReplyPhrase == "") {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_REPLY_PHRASE", "reply phrase is required and must be at most 500 characters")
	}
	templates, err := NormalizeGroupApplicationTemplates(policy.Templates)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_TEMPLATES", err.Error())
	}
	policy.Templates = templates
	if attachment != nil {
		if err := validateGroupApplicationAttachment(attachment); err != nil {
			return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_ATTACHMENT", err.Error())
		}
	}
	return s.repo.SavePolicy(ctx, policy, attachment, adminID)
}

func validateGroupApplicationAttachment(attachment *GroupApplicationAttachment) error {
	if attachment == nil {
		return nil
	}
	attachment.Filename = strings.TrimSpace(attachment.Filename)
	if attachment.Filename == "" || len(attachment.Filename) > 255 || strings.ContainsAny(attachment.Filename, "\r\n") {
		return errors.New("invalid attachment filename")
	}
	if len(attachment.Data) == 0 || len(attachment.Data) > GroupApplicationMaxAttachmentBytes {
		return fmt.Errorf("PDF attachment must be between 1 byte and %d bytes", GroupApplicationMaxAttachmentBytes)
	}
	if len(attachment.Data) < 5 || string(attachment.Data[:5]) != "%PDF-" {
		return errors.New("attachment must be a PDF")
	}
	attachment.ContentType = "application/pdf"
	attachment.ByteSize = int64(len(attachment.Data))
	sum := sha256.Sum256(attachment.Data)
	attachment.SHA256 = hex.EncodeToString(sum[:])
	return nil
}

func (s *GroupApplicationService) GetAttachment(ctx context.Context, attachmentID int64) (*GroupApplicationAttachment, error) {
	return s.repo.GetAttachment(ctx, attachmentID)
}

func (s *GroupApplicationService) ListApplications(ctx context.Context, filter GroupApplicationListFilter) (*GroupApplicationListResult, error) {
	return s.repo.ListApplications(ctx, filter)
}

func (s *GroupApplicationService) GetApplication(ctx context.Context, applicationID int64) (*GroupApplication, error) {
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	application.Mails, err = s.repo.ListApplicationMails(ctx, applicationID)
	return application, err
}

type groupApplicationInboundContent struct {
	Subject string `json:"subject,omitempty"`
	Text    string `json:"text,omitempty"`
}

func truncateGroupApplicationCommunication(value string, maxRunes int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	return string(runes[:maxRunes]), true
}

func (s *GroupApplicationService) protectInboundCommunication(subject, text string) (string, bool, error) {
	if s.encryptor == nil {
		return "", false, errors.New("group application communication encryption is unavailable")
	}
	subject, subjectTruncated := truncateGroupApplicationCommunication(strings.TrimSpace(subject), GroupApplicationMaxStoredSubjectRunes)
	text, textTruncated := truncateGroupApplicationCommunication(text, GroupApplicationMaxStoredReplyRunes)
	payload, err := json.Marshal(groupApplicationInboundContent{Subject: subject, Text: text})
	if err != nil {
		return "", false, fmt.Errorf("encode inbound group application communication: %w", err)
	}
	ciphertext, err := s.encryptor.Encrypt(string(payload))
	if err != nil {
		return "", false, fmt.Errorf("encrypt inbound group application communication: %w", err)
	}
	return ciphertext, subjectTruncated || textTruncated, nil
}

func (s *GroupApplicationService) ListCommunications(ctx context.Context, applicationID int64) ([]GroupApplicationCommunication, error) {
	if applicationID <= 0 {
		return nil, ErrGroupApplicationNotFound
	}
	if _, err := s.repo.GetApplication(ctx, applicationID); err != nil {
		return nil, err
	}
	items, err := s.repo.ListApplicationCommunications(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		item := &items[i]
		if item.Direction != GroupApplicationCommunicationInbound {
			continue
		}
		ciphertext := item.EncryptedContent
		item.EncryptedContent = ""
		if ciphertext == "" || s.encryptor == nil {
			item.ContentUnavailable = true
			continue
		}
		plaintext, decryptErr := s.encryptor.Decrypt(ciphertext)
		if decryptErr != nil {
			item.ContentUnavailable = true
			continue
		}
		var content groupApplicationInboundContent
		if json.Unmarshal([]byte(plaintext), &content) != nil {
			item.ContentUnavailable = true
			continue
		}
		item.Subject = content.Subject
		item.TextBody = content.Text
	}
	return items, nil
}

func (s *GroupApplicationService) Approve(ctx context.Context, applicationID, adminID int64) (*GroupApplication, error) {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return nil, err
	}
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	mail, err := s.buildMail(ctx, application, GroupApplicationMailApproval, "")
	if err != nil {
		return nil, err
	}
	mail.RequiredStatus = GroupApplicationStatusAwaitingReply
	attachmentID := application.AttachmentID
	mail.AttachmentID = &attachmentID
	return s.repo.Approve(ctx, applicationID, adminID, mail)
}

func (s *GroupApplicationService) Reject(ctx context.Context, applicationID, adminID int64, reason string) (*GroupApplication, error) {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 2000 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_REJECTION_REASON", "rejection reason is required and must be at most 2000 characters")
	}
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	mail, err := s.buildMail(ctx, application, GroupApplicationMailManualRejection, reason)
	if err != nil {
		return nil, err
	}
	mail.RequiredStatus = GroupApplicationStatusRejected
	return s.repo.Reject(ctx, applicationID, adminID, reason, mail)
}

func (s *GroupApplicationService) Revoke(ctx context.Context, applicationID, adminID int64, reason string) (*GroupApplication, error) {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 2000 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_REVOCATION_REASON", "revocation reason is required and must be at most 2000 characters")
	}
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	mail, err := s.buildMail(ctx, application, GroupApplicationMailRevocation, reason)
	if err != nil {
		return nil, err
	}
	mail.RequiredStatus = GroupApplicationStatusRevoked
	return s.repo.Revoke(ctx, applicationID, adminID, reason, mail)
}

func (s *GroupApplicationService) ResendApproval(ctx context.Context, applicationID int64) error {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return err
	}
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return err
	}
	if application.Status != GroupApplicationStatusAwaitingReply {
		return ErrGroupApplicationState
	}
	mail, err := s.buildMail(ctx, application, GroupApplicationMailApproval, "")
	if err != nil {
		return err
	}
	mail.RequiredStatus = GroupApplicationStatusAwaitingReply
	attachmentID := application.AttachmentID
	mail.AttachmentID = &attachmentID
	return s.repo.EnqueueMail(ctx, applicationID, mail)
}

func (s *GroupApplicationService) RetryMail(ctx context.Context, applicationID, outboxID int64) error {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return err
	}
	return s.repo.RetryMail(ctx, applicationID, outboxID)
}

func (s *GroupApplicationService) ProcessInboundReply(ctx context.Context, applicationID int64, fromAddress, reply string) (string, error) {
	if err := s.requireGroupApplicationWorkflow(ctx); err != nil {
		return "disabled", err
	}
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return "not_found", err
	}
	from, err := NormalizeGroupApplicationEmail(fromAddress)
	if err != nil || !strings.EqualFold(from, application.ContactEmail) {
		return "ignored_sender", nil
	}
	reply = NormalizeGroupApplicationReply(reply)
	if reply == application.ReplyPhraseSnapshot {
		mail, renderErr := s.buildMail(ctx, application, GroupApplicationMailCompletion, "")
		if renderErr != nil {
			return "error", renderErr
		}
		mail.RequiredStatus = GroupApplicationStatusCompleted
		_, err = s.repo.CompleteFromReply(ctx, applicationID, mail)
		if err != nil {
			return "error", err
		}
		return "completed", nil
	}
	mail, renderErr := s.buildMail(ctx, application, GroupApplicationMailReplyMismatch, "reply_mismatch")
	if renderErr != nil {
		return "error", renderErr
	}
	mail.RequiredStatus = GroupApplicationStatusRejected
	_, err = s.repo.RejectReplyMismatch(ctx, applicationID, mail)
	if err != nil {
		return "error", err
	}
	return "reply_mismatch", nil
}

func (s *GroupApplicationService) buildMail(ctx context.Context, application *GroupApplication, kind, decisionReason string) (GroupApplicationMailJob, error) {
	if application == nil {
		return GroupApplicationMailJob{}, ErrGroupApplicationNotFound
	}
	locale := NormalizeGroupApplicationLocale(application.Locale)
	localized := application.TemplatesSnapshot[kind]
	value, ok := localized[locale]
	if !ok {
		value = localized["zh"]
	}
	siteName := "Sub2API"
	if s.settingRepo != nil {
		if raw, err := s.settingRepo.GetValue(ctx, SettingKeySiteName); err == nil && strings.TrimSpace(raw) != "" {
			siteName = strings.TrimSpace(raw)
		}
	}
	variables := map[string]string{
		"site_name": siteName, "recipient": application.ContactEmail,
		"application_id": fmt.Sprintf("%d", application.ID), "group_name": application.GroupName,
		"application_reason": application.Reason, "reply_phrase": application.ReplyPhraseSnapshot,
		"attachment_name": application.AttachmentName, "decision_reason": decisionReason,
		"submitted_at": application.CreatedAt.Format(time.RFC3339),
	}
	if application.ReviewedAt != nil {
		variables["reviewed_at"] = application.ReviewedAt.Format(time.RFC3339)
	}
	subject, body, err := RenderGroupApplicationTemplate(value, variables)
	if err != nil {
		return GroupApplicationMailJob{}, err
	}
	return GroupApplicationMailJob{
		ApplicationID: application.ID, Kind: kind, Recipient: application.ContactEmail,
		Subject: subject, HTMLBody: body, MessageID: newGroupApplicationMessageID(kind, application.ID),
	}, nil
}
