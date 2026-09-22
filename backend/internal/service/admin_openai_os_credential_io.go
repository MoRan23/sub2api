package service

import "context"

// OpenAIOAuthAdminCredentialIO is available only to authenticated admin handlers.
// Private slot records must not enter ordinary account DTOs.
type OpenAIOAuthAdminCredentialIO interface {
	GetOpenAIOAuthOSCredential(context.Context, int64, string) (*OpenAIOAuthOSCredential, error)
	ListOpenAIOAuthOSCredentials(context.Context, int64) ([]*OpenAIOAuthOSCredential, error)
	BindOpenAIOAuthOSCredentials(context.Context, int64, string, map[string]any, string) (*OpenAIOAuthOSCredential, error)
}

func (s *adminServiceImpl) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsReader)
	if !ok {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	return repo.GetOpenAIOAuthOSCredential(ctx, id, os)
}

func (s *adminServiceImpl) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsReader)
	if !ok {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	return repo.ListOpenAIOAuthOSCredentials(ctx, id)
}

func (s *adminServiceImpl) BindOpenAIOAuthOSCredentials(ctx context.Context, id int64, os string, credentials map[string]any, source string) (*OpenAIOAuthOSCredential, error) {
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	return repo.BindOpenAIOAuthOSCredentials(ctx, id, os, credentials, source)
}
