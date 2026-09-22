package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type adminSharedAuthorizationRepository struct {
	*authorizationTestRepository
	revokedOS string
}

func (r *adminSharedAuthorizationRepository) RevokeOpenAIOAuthOSCredentials(_ context.Context, _ int64, os string) error {
	if NormalizeOpenAIOSFamily(os) == "" {
		return ErrOpenAIOAuthOSUnauthorized
	}
	r.revokedOS = os
	return nil
}

func TestAdminOpenAISharedRevokeResolvesDefaultIdentity(t *testing.T) {
	_, _, fixture := authorizationTestSetup(t)
	fixture.account.OpenAIOAuthOSProfiles.DefaultOS = OpenAIOSMacOS
	repo := &adminSharedAuthorizationRepository{authorizationTestRepository: fixture}
	admin := &adminServiceImpl{accountRepo: repo}
	require.NoError(t, admin.RevokeOpenAIOAuthOSCredentials(context.Background(), fixture.account.ID, ""))
	require.Equal(t, OpenAIOSMacOS, repo.revokedOS)
	require.NoError(t, admin.RevokeOpenAIOAuthOSCredentials(context.Background(), fixture.account.ID, OpenAIOSLinux))
	require.Equal(t, OpenAIOSLinux, repo.revokedOS)
}
