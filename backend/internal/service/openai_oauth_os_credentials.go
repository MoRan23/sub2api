package service

import (
	"context"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"maps"
	"strings"
	"time"
)

const (
	OpenAIOAuthAuthorizationUnauthorized   = "unauthorized"
	OpenAIOAuthAuthorizationAuthorized     = "authorized"
	OpenAIOAuthAuthorizationReauthRequired = "reauth_required"
)

var (
	// OS names/codes are retained for compatibility; authorization is account-wide.
	ErrOpenAIOAuthOSUnauthorized         = infraerrors.BadRequest("OPENAI_OAUTH_OS_UNAUTHORIZED", "OpenAI OAuth account authorization is unavailable")
	ErrOpenAIOAuthOSAuthorizationChanged = infraerrors.Conflict("OPENAI_OAUTH_OS_AUTHORIZATION_CHANGED", "OpenAI OAuth authorization changed; start a new request")
	ErrOpenAIOAuthOSSubjectMismatch      = infraerrors.BadRequest("OPENAI_OAUTH_OS_SUBJECT_MISMATCH", "OpenAI OAuth authorization must belong to the same ChatGPT account and user")
	ErrOpenAIOAuthOSRefreshTokenReused   = infraerrors.BadRequest("OPENAI_OAUTH_OS_REFRESH_TOKEN_REUSED", "OpenAI OAuth refresh token is already bound to another operating system")
)

// This is the only authorization data serialized in ordinary account profiles.
type OpenAIOAuthOSAuthorizationSummary struct {
	Status            string     `json:"status"`
	AuthorizedAt      *time.Time `json:"authorized_at,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	RefreshRetryAfter *time.Time `json:"refresh_retry_after,omitempty"`
}

func CloneOpenAIOAuthOSAuthorizationSummary(s OpenAIOAuthOSAuthorizationSummary) OpenAIOAuthOSAuthorizationSummary {
	clone := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		v := *t
		return &v
	}
	s.AuthorizedAt, s.ExpiresAt, s.RefreshRetryAfter = clone(s.AuthorizedAt), clone(s.ExpiresAt), clone(s.RefreshRetryAfter)
	return s
}

// Private database record. It must never be embedded in an API or scheduler DTO.
type OpenAIOAuthOSCredential struct {
	OwnerAccountID          int64          `json:"-"`
	OSFamily                string         `json:"-"`
	Credentials             map[string]any `json:"-"`
	AuthorizationGeneration string         `json:"-"`
	Revision                int64          `json:"-"`
	StateGeneration         string         `json:"-"`
	CredentialEpoch         string         `json:"-"`
	Status                  string         `json:"-"`
	AuthorizedAt            *time.Time     `json:"-"`
	ExpiresAt               *time.Time     `json:"-"`
	LastError               string         `json:"-"`
	RefreshRetryAfter       *time.Time     `json:"-"`
}

type OpenAIOAuthOSCredentialsReader interface {
	GetOpenAIOAuthOSCredential(context.Context, int64, string) (*OpenAIOAuthOSCredential, error)
	ListOpenAIOAuthOSCredentials(context.Context, int64) ([]*OpenAIOAuthOSCredential, error)
}

type OpenAIOAuthOSCredentialsRepository interface {
	OpenAIOAuthOSCredentialsReader
	BindOpenAIOAuthOSCredentials(context.Context, int64, string, map[string]any, string) (*OpenAIOAuthOSCredential, error)
	BindOpenAIOAuthOSCredentialsIfGeneration(context.Context, int64, string, string, map[string]any, string) (*OpenAIOAuthOSCredential, error)
	PatchOpenAIOAuthOSCredentialsIfUnchanged(context.Context, int64, string, string, int64, *int64, map[string]any, []string) (bool, error)
	SetOpenAIOAuthOSCredentialErrorIfUnchanged(context.Context, int64, string, string, int64, string) (bool, error)
	SetOpenAIOAuthOSCredentialCooldownIfUnchanged(context.Context, int64, string, string, int64, time.Time, string) (bool, error)
	RevokeOpenAIOAuthOSCredentials(context.Context, int64, string) error
	SetDefaultOpenAIOAuthOS(context.Context, int64, string) (*OpenAIOAuthOSProfiles, error)
}

type openAIOAuthCredentialModeChangeKey struct{}

// A snapshot containing auth_mode is not permission to replace provider-owned
// authorization. Only explicit admin credential-kind conversions create intent.
func WithOpenAIOAuthCredentialModeChangeIntent(ctx context.Context, ids ...int64) context.Context {
	allowed := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id > 0 {
			allowed[id] = true
		}
	}
	return context.WithValue(ctx, openAIOAuthCredentialModeChangeKey{}, allowed)
}

func OpenAIOAuthCredentialModeChangeAllowed(ctx context.Context, ids ...int64) bool {
	allowed, _ := ctx.Value(openAIOAuthCredentialModeChangeKey{}).(map[int64]bool)
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !allowed[id] {
			return false
		}
	}
	return true
}

func OpenAIOAuthProviderCredentialKeys() []string {
	return append([]string(nil), openAIRefreshCredentialKeys[:]...)
}

// Preserve provider-owned fields on all ordinary snapshot/configuration writes.
func PreserveOpenAIOAuthProviderCredentials(current, next map[string]any) map[string]any {
	out := maps.Clone(next)
	if out == nil {
		out = make(map[string]any)
	}
	for _, key := range OpenAIOAuthProviderCredentialKeys() {
		delete(out, key)
		if value, ok := current[key]; ok {
			out[key] = cloneOpenAIOAuthJSON(value)
		}
	}
	return out
}

func OpenAIOAuthProviderCredentials(credentials map[string]any) map[string]any {
	out := make(map[string]any)
	for _, key := range OpenAIOAuthProviderCredentialKeys() {
		if value, ok := credentials[key]; ok {
			out[key] = cloneOpenAIOAuthJSON(value)
		}
	}
	return out
}

func cloneOpenAIOAuthJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = cloneOpenAIOAuthJSON(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cloneOpenAIOAuthJSON(x)
		}
		return out
	default:
		return value
	}
}

func RequiresOpenAIOAuthOSAuthorization(account *Account) bool {
	return IsOpenAIOAuthOSProfileOwner(account) || account != nil && account.IsOpenAIOAuth() && account.IsShadow()
}

// OpenAIOAuthOSAuthorizationAvailable is retained for compatibility diagnostics.
// Scheduling never inspects tokens or OS authorization summaries.
func OpenAIOAuthOSAuthorizationAvailable(account *Account, _ string) bool {
	if !RequiresOpenAIOAuthOSAuthorization(account) {
		return true
	}
	return strings.TrimSpace(account.GetOpenAIAccessToken()) != "" || strings.TrimSpace(account.GetOpenAIRefreshToken()) != ""
}

// Resolve reads the single account credential snapshot and its private CAS
// attribution after account selection. Legacy authorization status and refresh
// cooldown fields are not another admission policy.
func ResolveOpenAIOAuthCredentialAccount(ctx context.Context, repo AccountRepository, account *Account, os string) (*Account, error) {
	if account == nil {
		return nil, ErrAccountNotFound
	}
	if !RequiresOpenAIOAuthOSAuthorization(account) {
		if account.OpenAIOAuthCredentialOS != "" {
			return nil, ErrOpenAIOAuthOSAuthorizationChanged
		}
		return account, nil
	}
	ownerID := account.ID
	if account.IsShadow() {
		ownerID = *account.ParentAccountID
	}
	owner := account
	if repo != nil {
		var err error
		owner, err = repo.GetByID(ctx, ownerID)
		if err != nil {
			return nil, err
		}
	} else if account.IsShadow() {
		return nil, ErrAccountNotFound
	}
	if !IsOpenAIOAuthOSProfileOwner(owner) {
		return nil, ErrAccountNotFound
	}
	os = NormalizeOpenAIOSFamily(os)
	if account.OpenAIOAuthCredentialOS != "" {
		if os != "" && os != account.OpenAIOAuthCredentialOS {
			return nil, ErrOpenAIOAuthOSAuthorizationChanged
		}
		os = account.OpenAIOAuthCredentialOS
	}
	if os == "" && owner.OpenAIOAuthOSProfiles != nil {
		os = NormalizeOpenAIOSFamily(owner.OpenAIOAuthOSProfiles.DefaultOS)
	}
	if os == "" {
		os = OpenAIOSWindows
	}
	reader, ok := repo.(OpenAIOAuthOSCredentialsReader)
	if !ok {
		return ResolveOpenAIOAuthIdentityAccount(ctx, repo, account, os)
	}
	slot, err := reader.GetOpenAIOAuthOSCredential(ctx, owner.ID, os)
	if err != nil {
		return nil, err
	}
	if slot == nil {
		if account.OpenAIOAuthAuthorizationGeneration != "" {
			return nil, ErrOpenAIOAuthOSAuthorizationChanged
		}
		return ResolveOpenAIOAuthIdentityAccount(ctx, repo, account, os)
	}
	if slot.OwnerAccountID != owner.ID {
		return nil, ErrOpenAIOAuthOSAuthorizationChanged
	}
	if account.OpenAIOAuthAuthorizationGeneration != "" && (account.OpenAIOAuthAuthorizationGeneration != slot.AuthorizationGeneration || account.OpenAIOAuthCredentialOwnerID != slot.OwnerAccountID) {
		return nil, ErrOpenAIOAuthOSAuthorizationChanged
	}
	business := account
	if !account.IsShadow() {
		business = owner
	}
	out := *business
	if business.Credentials != nil {
		out.Credentials = cloneOpenAIOAuthJSON(business.Credentials).(map[string]any)
	}
	// The compatibility reader returns an atomic projection of accounts.credentials
	// and its private metadata. It no longer reads a separate token store.
	out.Credentials = PreserveOpenAIOAuthProviderCredentials(slot.Credentials, out.Credentials)
	out.Extra = maps.Clone(business.Extra)
	if out.Extra == nil {
		out.Extra = make(map[string]any)
	}
	out.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(owner.OpenAIOAuthOSProfiles)
	if account.OpenAIOAuthCredentialOS != "" && OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles) {
		out.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(account.OpenAIOAuthOSProfiles)
	}
	out.Extra["codex_turn_state_generation"] = slot.StateGeneration
	out.Extra["codex_turn_state_credential_epoch"] = slot.CredentialEpoch
	out.OpenAIOAuthCredentialOS, out.OpenAIOAuthCredentialOwnerID = os, owner.ID
	out.OpenAIOAuthAuthorizationGeneration, out.OpenAIOAuthCredentialRevision = slot.AuthorizationGeneration, slot.Revision
	out.OpenAIOAuthCredentialStateGeneration, out.OpenAIOAuthCredentialEpoch = slot.StateGeneration, slot.CredentialEpoch
	return ResolveOpenAIOAuthIdentityAccount(ctx, repo, &out, os)
}

// ResolveOpenAIOAuthIdentityAccount only selects an installation profile. It
// does not select credentials, inspect authorization state, or affect scheduling.
func ResolveOpenAIOAuthIdentityAccount(ctx context.Context, repo AccountRepository, account *Account, os string) (*Account, error) {
	if account == nil {
		return nil, ErrAccountNotFound
	}
	if !RequiresOpenAIOAuthOSAuthorization(account) {
		return account, nil
	}
	owner := account
	if account.IsShadow() {
		if repo == nil {
			return nil, ErrAccountNotFound
		}
		parent, err := repo.GetByID(ctx, *account.ParentAccountID)
		if err != nil {
			return nil, err
		}
		owner, err = credentialAccountFromParent(account, parent)
		if err != nil {
			return nil, err
		}
	}
	if account.OpenAIOAuthCredentialOS != "" {
		if requested := NormalizeOpenAIOSFamily(os); requested != "" && requested != account.OpenAIOAuthCredentialOS {
			return nil, ErrOpenAIOAuthOSAuthorizationChanged
		}
		os = account.OpenAIOAuthCredentialOS
	}
	profiles := CloneOpenAIOAuthOSProfiles(owner.OpenAIOAuthOSProfiles)
	if account.OpenAIOAuthCredentialOS != "" && OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles) {
		profiles = CloneOpenAIOAuthOSProfiles(account.OpenAIOAuthOSProfiles)
	}
	if !OpenAIOAuthOSProfilesComplete(profiles) {
		if ensurer, supported := repo.(OpenAIOAuthOSProfilesEnsurer); supported {
			var err error
			profiles, err = ensurer.EnsureOpenAIOAuthOSProfiles(ctx, owner.ID)
			if err != nil || !OpenAIOAuthOSProfilesComplete(profiles) {
				return nil, ErrOpenAIOAuthOSProfileUnavailable
			}
		} else {
			// Older adapters keep their persisted single identity, as before OS
			// profiles were supported; never generate an ephemeral installation.
			if profiles != nil {
				return nil, ErrOpenAIOAuthOSProfileUnavailable
			}
			return account, nil
		}
	}
	if os = NormalizeOpenAIOSFamily(os); os == "" {
		os = profiles.DefaultOS
	}
	out := *account
	out.Credentials = maps.Clone(account.Credentials)
	if out.Credentials == nil {
		out.Credentials = make(map[string]any)
	}
	out.Extra = maps.Clone(account.Extra)
	if out.Extra == nil {
		out.Extra = make(map[string]any)
	}
	profile := profiles.Profiles[os]
	out.OpenAIOAuthOSProfiles = profiles
	out.Credentials["user_agent"] = profile.UserAgent
	out.Extra[openAIPinnedInstallationIDKey] = profile.InstallationID
	out.OpenAIOAuthCredentialOS, out.OpenAIOAuthCredentialOwnerID = os, owner.ID
	return &out, nil
}

func ReloadOpenAIOAuthCredentialAccount(ctx context.Context, repo AccountRepository, scoped *Account) (*Account, error) {
	if scoped == nil {
		return nil, ErrAccountNotFound
	}
	fresh, err := repo.GetByID(ctx, scoped.ID)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, ErrAccountNotFound
	}
	if scoped.OpenAIOAuthCredentialOS != "" {
		copy := *fresh
		copy.OpenAIOAuthCredentialOS = scoped.OpenAIOAuthCredentialOS
		copy.OpenAIOAuthCredentialOwnerID = scoped.OpenAIOAuthCredentialOwnerID
		copy.OpenAIOAuthAuthorizationGeneration = scoped.OpenAIOAuthAuthorizationGeneration
		copy.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(scoped.OpenAIOAuthOSProfiles)
		fresh = &copy
	}
	return ResolveOpenAIOAuthCredentialAccount(ctx, repo, fresh, scoped.OpenAIOAuthCredentialOS)
}

func OpenAIOAuthCredentialSubject(credentials map[string]any, key string) string {
	v, _ := credentials[key].(string)
	return strings.TrimSpace(v)
}
