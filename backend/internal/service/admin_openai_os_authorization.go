package service

import (
	"context"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIOAuthOSAuthorizationAdmin exposes dedicated mutations without widening
// the general AdminService credentials update contract.
type OpenAIOAuthOSAuthorizationAdmin interface {
	ResolveOpenAIOAuthCredentialAccount(context.Context, int64, string) (*Account, error)
	RevokeOpenAIOAuthOSCredentials(context.Context, int64, string) error
	SetDefaultOpenAIOAuthOS(context.Context, int64, string) (*OpenAIOAuthOSProfiles, error)
}

func (s *adminServiceImpl) ResolveOpenAIOAuthCredentialAccount(ctx context.Context, id int64, os string) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account, os)
}

func (s *adminServiceImpl) RevokeOpenAIOAuthOSCredentials(ctx context.Context, id int64, os string) error {
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	if os == "" {
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		os = OpenAIOSWindows
		if account != nil && account.OpenAIOAuthOSProfiles != nil {
			os = account.OpenAIOAuthOSProfiles.DefaultOS
		}
	}
	return repo.RevokeOpenAIOAuthOSCredentials(ctx, id, os)
}

func (s *adminServiceImpl) SetDefaultOpenAIOAuthOS(ctx context.Context, id int64, os string) (*OpenAIOAuthOSProfiles, error) {
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	return repo.SetDefaultOpenAIOAuthOS(ctx, id, os)
}
