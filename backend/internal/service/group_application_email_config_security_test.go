//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func groupApplicationEmailConfigInput(config GroupApplicationEmailConfig) GroupApplicationEmailConfigInput {
	return GroupApplicationEmailConfigInput{
		Enabled: config.Enabled,
		SMTP: GroupApplicationSMTPConfigInput{
			GroupApplicationSMTPConfig: config.SMTP,
		},
		IMAP: GroupApplicationIMAPConfigInput{
			GroupApplicationIMAPConfig: config.IMAP,
		},
	}
}

func TestGroupApplicationEmailConfigInputDecodesNestedPasswordActions(t *testing.T) {
	var input GroupApplicationEmailConfigInput
	require.NoError(t, json.Unmarshal([]byte(`{
		"enabled":false,
		"smtp":{"host":"smtp.example.com","port":465,"username":"sender@example.com","password_action":"clear"},
		"imap":{"host":"imap.example.com","port":993,"mailbox":"INBOX","password_action":"keep"}
	}`), &input))
	require.Equal(t, "smtp.example.com", input.SMTP.Host)
	require.Equal(t, 465, input.SMTP.Port)
	require.Equal(t, GroupApplicationPasswordClear, input.SMTP.PasswordAction)
	require.Equal(t, "imap.example.com", input.IMAP.Host)
	require.Equal(t, GroupApplicationPasswordKeep, input.IMAP.PasswordAction)
}

func TestGroupApplicationSharedSMTPCredentialsCannotBeKeptForChangedIMAPEndpoint(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	input := groupApplicationEmailConfigInput(*public)
	input.IMAP.UseSMTPCredentials = true
	input.IMAP.Host = "attacker.example.com"

	_, err = svc.ResolveEmailConfigInputForTest(context.Background(), input, "imap")
	require.Error(t, err)
	require.Equal(t, "INVALID_GROUP_APPLICATION_EMAIL_CONFIG", infraerrors.Reason(err))
	require.ErrorContains(t, err, "SMTP credential reuse was newly enabled")

	_, err = svc.SaveEmailConfigInput(context.Background(), input)
	require.Error(t, err)
	require.ErrorContains(t, err, "SMTP credential reuse was newly enabled")
}

func TestGroupApplicationChangedIMAPEndpointAcceptsExplicitSMTPPasswordReplacement(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	input := groupApplicationEmailConfigInput(*public)
	input.IMAP.UseSMTPCredentials = true
	input.IMAP.Host = "new-imap.example.com"
	input.SMTP.Password = "re-entered-secret"
	input.SMTP.PasswordAction = GroupApplicationPasswordReplace

	resolved, err := svc.ResolveEmailConfigInputForTest(context.Background(), input, "imap")
	require.NoError(t, err)
	require.Equal(t, "new-imap.example.com", resolved.IMAP.Host)
	require.Equal(t, resolved.SMTP.Username, resolved.IMAP.Username)
	require.Equal(t, "re-entered-secret", resolved.IMAP.Password)
}

func TestGroupApplicationSharedSMTPCredentialsCanBeKeptForUnchangedIMAPEndpoint(t *testing.T) {
	repo := &groupApplicationSettingRepoStub{values: map[string]string{}}
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	config := validGroupApplicationEmailConfig()
	config.IMAP.UseSMTPCredentials = true
	_, err := svc.SaveEmailConfig(context.Background(), config)
	require.NoError(t, err)
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	resolved, err := svc.ResolveEmailConfigInputForTest(
		context.Background(),
		groupApplicationEmailConfigInput(*public),
		"imap",
	)
	require.NoError(t, err)
	require.Equal(t, "smtp-secret", resolved.IMAP.Password)
}

func TestGroupApplicationPasswordKeepIsBoundToTransportEndpoint(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	input := groupApplicationEmailConfigInput(*public)
	input.SMTP.Port = 465
	input.SMTP.TLSMode = "implicit"
	_, err = svc.ResolveEmailConfigInputForTest(context.Background(), input, "smtp")
	require.ErrorContains(t, err, "SMTP endpoint changed")

	input = groupApplicationEmailConfigInput(*public)
	input.IMAP.Host = "imap2.example.com"
	_, err = svc.ResolveEmailConfigInputForTest(context.Background(), input, "imap")
	require.ErrorContains(t, err, "IMAP endpoint changed")
}

func TestGroupApplicationPasswordActionsClearStoredCredentialsWhenDisabled(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	input := groupApplicationEmailConfigInput(*public)
	input.Enabled = false
	input.SMTP.PasswordAction = GroupApplicationPasswordClear
	input.IMAP.PasswordAction = GroupApplicationPasswordClear
	saved, err := svc.SaveEmailConfigInput(context.Background(), input)
	require.NoError(t, err)
	require.False(t, saved.SMTP.PasswordConfigured)
	require.False(t, saved.IMAP.PasswordConfigured)

	var stored storedGroupApplicationEmailConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyGroupApplicationEmail]), &stored))
	require.Empty(t, stored.SMTP.EncryptedPassword)
	require.Empty(t, stored.IMAP.EncryptedPassword)
}

func TestGroupApplicationPasswordClearIsRejectedWhileEnabled(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	input := groupApplicationEmailConfigInput(*public)
	input.SMTP.PasswordAction = GroupApplicationPasswordClear
	_, err = svc.SaveEmailConfigInput(context.Background(), input)
	require.Error(t, err)
	require.Equal(t, "INVALID_GROUP_APPLICATION_EMAIL_CONFIG", infraerrors.Reason(err))
	require.ErrorContains(t, err, "SMTP host, username, password")
}

func TestGroupApplicationOmittedPasswordActionsRemainBackwardCompatible(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	saved, err := svc.SaveEmailConfigInput(context.Background(), groupApplicationEmailConfigInput(*public))
	require.NoError(t, err)
	require.True(t, saved.SMTP.PasswordConfigured)
	require.True(t, saved.IMAP.PasswordConfigured)
}

func TestGroupApplicationPasswordActionValidation(t *testing.T) {
	repo := configuredGroupApplicationSettings(t)
	svc := NewGroupApplicationService(nil, repo, groupApplicationEncryptorStub{})
	public, err := svc.GetEmailConfig(context.Background())
	require.NoError(t, err)

	input := groupApplicationEmailConfigInput(*public)
	input.SMTP.PasswordAction = GroupApplicationPasswordReplace
	_, err = svc.SaveEmailConfigInput(context.Background(), input)
	require.ErrorContains(t, err, "password is required")

	input = groupApplicationEmailConfigInput(*public)
	input.IMAP.PasswordAction = GroupApplicationPasswordAction("rotate")
	_, err = svc.SaveEmailConfigInput(context.Background(), input)
	require.ErrorContains(t, err, "keep, replace or clear")

	input = groupApplicationEmailConfigInput(*public)
	input.SMTP.Password = "********"
	input.SMTP.PasswordAction = GroupApplicationPasswordReplace
	_, err = svc.SaveEmailConfigInput(context.Background(), input)
	require.ErrorContains(t, err, "password is required")
}
