package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// GroupApplicationPasswordAction makes secret retention explicit while
// preserving the legacy omitted-field behavior for existing API clients.
type GroupApplicationPasswordAction string

const (
	GroupApplicationPasswordKeep    GroupApplicationPasswordAction = "keep"
	GroupApplicationPasswordReplace GroupApplicationPasswordAction = "replace"
	GroupApplicationPasswordClear   GroupApplicationPasswordAction = "clear"
)

type GroupApplicationSMTPConfigInput struct {
	GroupApplicationSMTPConfig
	PasswordAction GroupApplicationPasswordAction `json:"password_action,omitempty"`
}

type GroupApplicationIMAPConfigInput struct {
	GroupApplicationIMAPConfig
	PasswordAction GroupApplicationPasswordAction `json:"password_action,omitempty"`
}

// GroupApplicationEmailConfigInput is the write/test API contract. An omitted
// password_action remains backward compatible: a non-empty password replaces
// the secret, while an empty password keeps it.
type GroupApplicationEmailConfigInput struct {
	Enabled bool                            `json:"enabled"`
	SMTP    GroupApplicationSMTPConfigInput `json:"smtp"`
	IMAP    GroupApplicationIMAPConfigInput `json:"imap"`
}

func (input GroupApplicationEmailConfigInput) Config() GroupApplicationEmailConfig {
	return GroupApplicationEmailConfig{
		Enabled: input.Enabled,
		SMTP:    input.SMTP.GroupApplicationSMTPConfig,
		IMAP:    input.IMAP.GroupApplicationIMAPConfig,
	}
}

func groupApplicationPasswordAction(action GroupApplicationPasswordAction, password string) (GroupApplicationPasswordAction, error) {
	if action == "" {
		if password == "" || password == "********" {
			return GroupApplicationPasswordKeep, nil
		}
		return GroupApplicationPasswordReplace, nil
	}

	switch action {
	case GroupApplicationPasswordKeep:
		if password != "" && password != "********" {
			return "", errors.New("password must be empty when password_action is keep")
		}
	case GroupApplicationPasswordReplace:
		if password == "" || password == "********" {
			return "", errors.New("password is required when password_action is replace")
		}
	case GroupApplicationPasswordClear:
		if password != "" {
			return "", errors.New("password must be empty when password_action is clear")
		}
	default:
		return "", errors.New("password_action must be keep, replace or clear")
	}
	return action, nil
}

func sameGroupApplicationTransportEndpoint(
	oldHost string,
	oldPort int,
	oldUsername string,
	oldTLSMode string,
	host string,
	port int,
	username string,
	tlsMode string,
) bool {
	return strings.EqualFold(strings.TrimSpace(oldHost), strings.TrimSpace(host)) &&
		oldPort == port &&
		strings.TrimSpace(oldUsername) == strings.TrimSpace(username) &&
		strings.EqualFold(strings.TrimSpace(oldTLSMode), strings.TrimSpace(tlsMode))
}

func sameGroupApplicationIMAPCredentialEndpoint(old storedGroupApplicationIMAPConfig, current storedGroupApplicationIMAPConfig) bool {
	return strings.EqualFold(strings.TrimSpace(old.Host), strings.TrimSpace(current.Host)) &&
		old.Port == current.Port &&
		strings.EqualFold(strings.TrimSpace(old.TLSMode), strings.TrimSpace(current.TLSMode))
}

func (s *GroupApplicationService) applyGroupApplicationPasswordAction(
	label string,
	action GroupApplicationPasswordAction,
	password string,
	existingCiphertext string,
	endpointUnchanged bool,
) (string, error) {
	switch action {
	case GroupApplicationPasswordKeep:
		if existingCiphertext != "" && !endpointUnchanged {
			return "", fmt.Errorf("%s endpoint changed; enter the password again with password_action=replace", label)
		}
		return existingCiphertext, nil
	case GroupApplicationPasswordReplace:
		if s.encryptor == nil {
			return "", errors.New("group application email encryption is unavailable")
		}
		encrypted, err := s.encryptor.Encrypt(password)
		if err != nil {
			return "", fmt.Errorf("encrypt %s password: %w", label, err)
		}
		return encrypted, nil
	case GroupApplicationPasswordClear:
		return "", nil
	default:
		return "", errors.New("unsupported group application password action")
	}
}

func (s *GroupApplicationService) buildStoredEmailConfigInput(
	ctx context.Context,
	input GroupApplicationEmailConfigInput,
	passwordScope string,
) (storedGroupApplicationEmailConfig, error) {
	if passwordScope != "" && passwordScope != "smtp" && passwordScope != "imap" {
		return storedGroupApplicationEmailConfig{}, errors.New("unsupported group application email transport")
	}

	config := normalizeGroupApplicationEmailConfig(input.Config())
	existing, _, err := s.loadStoredEmailConfig(ctx)
	if err != nil {
		return storedGroupApplicationEmailConfig{}, err
	}
	stored := storedGroupApplicationEmailConfig{
		Enabled: config.Enabled,
		SMTP: storedGroupApplicationSMTPConfig{
			Host: config.SMTP.Host, Port: config.SMTP.Port, Username: config.SMTP.Username,
			FromAddress: config.SMTP.FromAddress, FromName: config.SMTP.FromName, TLSMode: config.SMTP.TLSMode,
		},
		IMAP: storedGroupApplicationIMAPConfig{
			Host: config.IMAP.Host, Port: config.IMAP.Port, Username: config.IMAP.Username,
			UseSMTPCredentials: config.IMAP.UseSMTPCredentials, Mailbox: config.IMAP.Mailbox,
			ReplyAddress: config.IMAP.ReplyAddress, TLSMode: config.IMAP.TLSMode,
			PollIntervalSeconds: config.IMAP.PollIntervalSeconds,
		},
	}

	smtpAction, err := groupApplicationPasswordAction(input.SMTP.PasswordAction, config.SMTP.Password)
	if err != nil {
		return storedGroupApplicationEmailConfig{}, fmt.Errorf("SMTP %w", err)
	}
	imapAction, err := groupApplicationPasswordAction(input.IMAP.PasswordAction, config.IMAP.Password)
	if err != nil {
		return storedGroupApplicationEmailConfig{}, fmt.Errorf("IMAP %w", err)
	}

	resolveSMTP := passwordScope == "" || passwordScope == "smtp" || passwordScope == "imap" && stored.IMAP.UseSMTPCredentials
	if resolveSMTP {
		stored.SMTP.EncryptedPassword, err = s.applyGroupApplicationPasswordAction(
			"SMTP",
			smtpAction,
			config.SMTP.Password,
			existing.SMTP.EncryptedPassword,
			sameGroupApplicationTransportEndpoint(
				existing.SMTP.Host, existing.SMTP.Port, existing.SMTP.Username, existing.SMTP.TLSMode,
				stored.SMTP.Host, stored.SMTP.Port, stored.SMTP.Username, stored.SMTP.TLSMode,
			),
		)
		if err != nil {
			return storedGroupApplicationEmailConfig{}, err
		}
	}

	if stored.IMAP.UseSMTPCredentials {
		if input.IMAP.PasswordAction == GroupApplicationPasswordReplace {
			return storedGroupApplicationEmailConfig{}, errors.New("IMAP password cannot be replaced while SMTP credentials are reused")
		}
		stored.IMAP.Username = ""
		stored.IMAP.EncryptedPassword = ""
		if (passwordScope == "" || passwordScope == "imap") &&
			smtpAction == GroupApplicationPasswordKeep &&
			existing.SMTP.EncryptedPassword != "" &&
			(!existing.IMAP.UseSMTPCredentials || !sameGroupApplicationIMAPCredentialEndpoint(existing.IMAP, stored.IMAP)) {
			return storedGroupApplicationEmailConfig{}, errors.New("IMAP endpoint changed or SMTP credential reuse was newly enabled; enter the SMTP password again with password_action=replace")
		}
	} else if passwordScope == "" || passwordScope == "imap" {
		stored.IMAP.EncryptedPassword, err = s.applyGroupApplicationPasswordAction(
			"IMAP",
			imapAction,
			config.IMAP.Password,
			existing.IMAP.EncryptedPassword,
			sameGroupApplicationTransportEndpoint(
				existing.IMAP.Host, existing.IMAP.Port, existing.IMAP.Username, existing.IMAP.TLSMode,
				stored.IMAP.Host, stored.IMAP.Port, stored.IMAP.Username, stored.IMAP.TLSMode,
			),
		)
		if err != nil {
			return storedGroupApplicationEmailConfig{}, err
		}
	}

	return stored, nil
}

func (s *GroupApplicationService) SaveEmailConfigInput(ctx context.Context, input GroupApplicationEmailConfigInput) (*GroupApplicationEmailConfig, error) {
	if s.settingRepo == nil || s.encryptor == nil {
		return nil, errors.New("group application email settings are unavailable")
	}
	stored, err := s.buildStoredEmailConfigInput(ctx, input, "")
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	if err := validateStoredGroupApplicationEmailConfig(stored, stored.Enabled); err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGroupApplicationEmail, string(raw)); err != nil {
		return nil, err
	}
	return publicGroupApplicationEmailConfig(stored, false), nil
}

func (s *GroupApplicationService) ResolveEmailConfigInputForTest(
	ctx context.Context,
	input GroupApplicationEmailConfigInput,
	transport string,
) (*GroupApplicationEmailConfig, error) {
	stored, err := s.buildStoredEmailConfigInput(ctx, input, transport)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	switch transport {
	case "smtp":
		err = validateStoredGroupApplicationSMTPConfig(stored.SMTP, true)
	case "imap":
		err = validateStoredGroupApplicationIMAPConfig(stored.IMAP, stored.SMTP, true)
	default:
		err = errors.New("unsupported group application email transport")
	}
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_APPLICATION_EMAIL_CONFIG", err.Error())
	}
	stored.Enabled = false
	return s.runtimeEmailConfig(stored, false)
}
