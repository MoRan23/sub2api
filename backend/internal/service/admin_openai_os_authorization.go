package service

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type openAIOAuthAccountStateIntentKey struct{}

// WithOpenAIOAuthAccountStateIntent marks explicit administrator state changes.
// Credential refreshes and stale account snapshots do not carry this intent.
func WithOpenAIOAuthAccountStateIntent(ctx context.Context, ids ...int64) context.Context {
	allowed := make(map[int64]struct{})
	if previous, ok := ctx.Value(openAIOAuthAccountStateIntentKey{}).(map[int64]struct{}); ok {
		for id := range previous {
			allowed[id] = struct{}{}
		}
	}
	for _, id := range ids {
		if id > 0 {
			allowed[id] = struct{}{}
		}
	}
	return context.WithValue(ctx, openAIOAuthAccountStateIntentKey{}, allowed)
}

func OpenAIOAuthAccountStateIntentAllowed(ctx context.Context, id int64) bool {
	allowed, _ := ctx.Value(openAIOAuthAccountStateIntentKey{}).(map[int64]struct{})
	_, ok := allowed[id]
	return id > 0 && ok
}

// OpenAIOAuthCredentialsAdmin accepts credentials from a completed OAuth flow.
// The optional OS selects client identity only, never a separate authorization.
type OpenAIOAuthCredentialsAdmin interface {
	BindOpenAIOAuthCredentials(context.Context, int64, string, map[string]any) (*Account, error)
}

func (s *adminServiceImpl) BindOpenAIOAuthCredentials(ctx context.Context, id int64, os string, credentials map[string]any) (*Account, error) {
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return nil, infraerrors.BadRequest("NOT_OPENAI_OAUTH", "authorization requires a regular OpenAI OAuth account")
	}
	if strings.TrimSpace(os) != "" && NormalizeOpenAIOSFamily(os) == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_OS_INVALID", "invalid client identity OS")
	}
	os = NormalizeOpenAIOSFamily(os)
	if os == "" && account.OpenAIOAuthOSProfiles != nil {
		os = NormalizeOpenAIOSFamily(account.OpenAIOAuthOSProfiles.DefaultOS)
	}
	if os == "" {
		os = OpenAIOSWindows
	}
	credentials = SanitizeStoredCredentials(account.Platform, credentials)
	if OpenAIOAuthCredentialSubject(credentials, "access_token") == "" && OpenAIOAuthCredentialSubject(credentials, "refresh_token") == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_CREDENTIALS_REQUIRED", "access or refresh token is required")
	}
	if _, err = repo.BindOpenAIOAuthOSCredentials(ctx, id, os, credentials, "reauthorization"); err != nil {
		return nil, err
	}
	return s.accountRepo.GetByID(ctx, id)
}

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
