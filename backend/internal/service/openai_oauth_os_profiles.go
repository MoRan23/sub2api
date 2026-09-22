package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

const (
	OpenAIOSWindows = "windows"
	OpenAIOSMacOS   = "macos"
	OpenAIOSLinux   = "linux"
)

// Persistence errors may contain SQL arguments. The request boundary exposes
// one stable error and never emits newly generated, uncommitted identities.
var ErrOpenAIOAuthOSProfileUnavailable = errors.New("OpenAI OAuth OS identity is unavailable")

// OpenAIOAuthOSProfile is an account-owned installation and its matching client
// environment. SyncSessionID is independent of the rotating daily roots.
type OpenAIOAuthOSProfile struct {
	OSFamily       string                            `json:"os"`
	InstallationID string                            `json:"installation_id"`
	UserAgent      string                            `json:"user_agent"`
	SyncSessionID  string                            `json:"sync_session_id"`
	Authorization  OpenAIOAuthOSAuthorizationSummary `json:"authorization"`
}

type OpenAIOAuthOSProfiles struct {
	DefaultOS string                          `json:"default_os"`
	Profiles  map[string]OpenAIOAuthOSProfile `json:"profiles"`
	// One grant is shared by all installation identities. Per-profile summaries
	// remain compatibility projections, not separate authorizations.
	Authorization *OpenAIOAuthOSAuthorizationSummary `json:"authorization,omitempty"`
}

type OpenAIOAuthOSProfilesEnsurer interface {
	EnsureOpenAIOAuthOSProfiles(context.Context, int64) (*OpenAIOAuthOSProfiles, error)
}

type OpenAIOAuthOSProfilesReader interface {
	GetOpenAIOAuthOSProfiles(context.Context, int64) (*OpenAIOAuthOSProfiles, error)
}

type OpenAIOAuthOSProfilesRepository interface {
	OpenAIOAuthOSProfilesEnsurer
	OpenAIOAuthOSProfilesReader
}

type OpenAIOAuthOSProfilesBackfiller interface {
	BackfillOpenAIOAuthOSProfiles(context.Context) error
}

type OpenAIOAuthOSProfileInstallationRegenerator interface {
	RegenerateOpenAIOAuthOSProfileInstallationID(context.Context, int64, string) (*OpenAIOAuthOSProfiles, error)
}

type OpenAIOAuthOSProfileAdminRegenerator interface {
	RegenerateOpenAIInstallationIDForOS(context.Context, int64, string) (*OpenAIOAuthOSProfiles, error)
}

func NormalizeOpenAIOSFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OpenAIOSWindows:
		return OpenAIOSWindows
	case OpenAIOSMacOS:
		return OpenAIOSMacOS
	case OpenAIOSLinux:
		return OpenAIOSLinux
	default:
		return ""
	}
}

func OpenAIOAuthOSFamilies() []string {
	return []string{OpenAIOSWindows, OpenAIOSMacOS, OpenAIOSLinux}
}

func IsOpenAIOAuthOSProfileOwner(account *Account) bool {
	return account != nil && account.IsOpenAIOAuth() && !account.IsShadow() &&
		!account.IsOpenAIPersonalAccessToken() && !account.IsOpenAIAgentIdentity() &&
		!strings.EqualFold(strings.TrimSpace(account.GetCredential(openAIAuthModeLegacyCredentialKey)), OpenAIAuthModeAgentIdentity)
}

func CloneOpenAIOAuthOSProfiles(value *OpenAIOAuthOSProfiles) *OpenAIOAuthOSProfiles {
	if value == nil {
		return nil
	}
	out := &OpenAIOAuthOSProfiles{DefaultOS: value.DefaultOS, Profiles: maps.Clone(value.Profiles)}
	if value.Authorization != nil {
		summary := CloneOpenAIOAuthOSAuthorizationSummary(*value.Authorization)
		out.Authorization = &summary
	}
	for os, profile := range out.Profiles {
		profile.Authorization = CloneOpenAIOAuthOSAuthorizationSummary(profile.Authorization)
		out.Profiles[os] = profile
	}
	return out
}

func defaultOpenAIOAuthEnvironment(osFamily string) string {
	switch osFamily {
	case OpenAIOSMacOS:
		return "(Mac OS 26.6.2; arm64) iTerm.app/3.7.0"
	case OpenAIOSLinux:
		return codexCLIEnvironmentFingerprint
	default:
		return "(Windows 10.0.26200; x86_64) WindowsTerminal"
	}
}

func validOpenAIOAuthProfileUA(ua, osFamily string) bool {
	return OpenAIEnvironmentFingerprintFromUserAgent(ua) != "" && openai.DetectOSFamilyFromUserAgent(ua) == osFamily
}

func validOpenAIOAuthSyncSessionID(value string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		return ""
	}
	return parsed.String()
}

func validOpenAIOAuthProfileInstallationID(value string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		return ""
	}
	return parsed.String()
}

// BuildOpenAIOAuthOSProfiles is a pure repair operation. The repository calls it
// under the owner row lock, so legacy UA, installation and sync root describe the
// same current identity. Existing valid slots always win over legacy mirrors.
func BuildOpenAIOAuthOSProfiles(account *Account, existing *OpenAIOAuthOSProfiles, legacySyncSessionID ...string) (*OpenAIOAuthOSProfiles, error) {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return nil, fmt.Errorf("OpenAI OS profiles require a regular OAuth credential owner")
	}
	out := CloneOpenAIOAuthOSProfiles(existing)
	if out == nil {
		out = &OpenAIOAuthOSProfiles{}
	}
	if out.Profiles == nil {
		out.Profiles = make(map[string]OpenAIOAuthOSProfile, 3)
	}
	out.DefaultOS = NormalizeOpenAIOSFamily(out.DefaultOS)
	if out.DefaultOS == "" {
		out.DefaultOS = openai.DetectOSFamilyFromUserAgent(account.GetOpenAIUserAgent())
		if out.DefaultOS == "" {
			out.DefaultOS = OpenAIOSWindows
		}
	}
	legacyInstallation := validOpenAIOAuthProfileInstallationID(account.GetPinnedOpenAIInstallationID())
	legacySync := ""
	if len(legacySyncSessionID) > 0 {
		legacySync = validOpenAIOAuthSyncSessionID(legacySyncSessionID[0])
	}
	seenInstallation := make(map[string]bool, 3)
	seenSync := make(map[string]bool, 3)
	for _, osFamily := range OpenAIOAuthOSFamilies() {
		profile := out.Profiles[osFamily]
		profile.OSFamily = osFamily
		if profile.Authorization.Status == "" {
			profile.Authorization.Status = OpenAIOAuthAuthorizationUnauthorized
		}
		profile.InstallationID = validOpenAIOAuthProfileInstallationID(profile.InstallationID)
		if profile.InstallationID == "" && osFamily == out.DefaultOS {
			profile.InstallationID = legacyInstallation
		}
		if profile.InstallationID == "" || seenInstallation[profile.InstallationID] {
			profile.InstallationID = uuid.NewString()
		}
		seenInstallation[profile.InstallationID] = true
		profile.SyncSessionID = validOpenAIOAuthSyncSessionID(profile.SyncSessionID)
		if profile.SyncSessionID == "" && osFamily == out.DefaultOS {
			profile.SyncSessionID = legacySync
		}
		if profile.SyncSessionID == "" || seenSync[profile.SyncSessionID] {
			generated, err := uuid.NewV7()
			if err != nil {
				return nil, fmt.Errorf("generate OpenAI %s sync identity: %w", osFamily, err)
			}
			profile.SyncSessionID = generated.String()
		}
		seenSync[profile.SyncSessionID] = true
		if !validOpenAIOAuthProfileUA(profile.UserAgent, osFamily) {
			legacyUA := strings.TrimSpace(account.GetOpenAIUserAgent())
			if osFamily == out.DefaultOS && validOpenAIOAuthProfileUA(legacyUA, osFamily) {
				profile.UserAgent = legacyUA
			} else {
				ua, err := BuildOpenAIUserAgentWithEnvironment(legacyUA, defaultOpenAIOAuthEnvironment(osFamily))
				if err != nil {
					return nil, err
				}
				profile.UserAgent = ua
			}
		}
		out.Profiles[osFamily] = profile
	}
	for key := range out.Profiles {
		if NormalizeOpenAIOSFamily(key) != key || key == "" {
			delete(out.Profiles, key)
		}
	}
	return out, nil
}

func OpenAIOAuthOSProfilesComplete(profiles *OpenAIOAuthOSProfiles) bool {
	if profiles == nil || NormalizeOpenAIOSFamily(profiles.DefaultOS) == "" || len(profiles.Profiles) != 3 {
		return false
	}
	installations, syncRoots := make(map[string]bool, 3), make(map[string]bool, 3)
	for _, osFamily := range OpenAIOAuthOSFamilies() {
		profile, exists := profiles.Profiles[osFamily]
		installation, syncRoot := validOpenAIOAuthProfileInstallationID(profile.InstallationID), validOpenAIOAuthSyncSessionID(profile.SyncSessionID)
		if !exists || profile.OSFamily != osFamily || installation == "" || syncRoot == "" ||
			installations[installation] || syncRoots[syncRoot] || !validOpenAIOAuthProfileUA(profile.UserAgent, osFamily) {
			return false
		}
		installations[installation], syncRoots[syncRoot] = true, true
	}
	return true
}

// ApplyOpenAIOAuthOSProfiles maintains the legacy default mirrors without sharing
// a mutable profile map with a scheduler snapshot or a previous request.
func ApplyOpenAIOAuthOSProfiles(account *Account, profiles *OpenAIOAuthOSProfiles) {
	if account == nil {
		return
	}
	account.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(profiles)
	if !IsOpenAIOAuthOSProfileOwner(account) || profiles == nil {
		return
	}
	profile, exists := profiles.Profiles[profiles.DefaultOS]
	if !exists {
		return
	}
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Credentials = maps.Clone(account.Credentials)
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	account.Extra[openAIPinnedInstallationIDKey] = profile.InstallationID
	account.Credentials["user_agent"] = profile.UserAgent
}

// PrepareOpenAIOAuthOSProfilesForCreate ignores any caller-supplied identity.
// New accounts use their explicit initial authorization OS, with Windows kept
// as the compatibility default when older creation callers omit it.
func PrepareOpenAIOAuthOSProfilesForCreate(account *Account) error {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return nil
	}
	seed := *account
	seed.Extra = maps.Clone(account.Extra)
	delete(seed.Extra, openAIPinnedInstallationIDKey)
	seed.Credentials = maps.Clone(account.Credentials)
	delete(seed.Credentials, "user_agent")
	initialOS := NormalizeOpenAIOSFamily(account.OpenAIOAuthInitialOS)
	if strings.TrimSpace(account.OpenAIOAuthInitialOS) != "" && initialOS == "" {
		return ErrOpenAIOAuthOSUnauthorized
	}
	if initialOS == "" {
		initialOS = OpenAIOSWindows
	}
	profiles, err := BuildOpenAIOAuthOSProfiles(&seed, &OpenAIOAuthOSProfiles{DefaultOS: initialOS})
	if err != nil {
		return err
	}
	ApplyOpenAIOAuthOSProfiles(account, profiles)
	return nil
}

// ResolveOpenAIOAuthOSProfile returns one immutable request profile. Spark uses
// its credential owner, and unknown OS evidence falls back to the stored default.
func ResolveOpenAIOAuthOSProfile(ctx context.Context, repo AccountRepository, account *Account, osFamily string) (OpenAIOAuthOSProfile, error) {
	if account != nil && account.OpenAIOAuthCredentialOS != "" {
		if explicit := NormalizeOpenAIOSFamily(osFamily); explicit != "" && explicit != account.OpenAIOAuthCredentialOS {
			return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSAuthorizationChanged
		}
		osFamily = account.OpenAIOAuthCredentialOS
	}
	if account != nil && account.IsShadow() && repo == nil {
		return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSProfileUnavailable
	}
	owner, err := resolveOpenAIInstallationIdentityAccount(ctx, repo, account)
	if err != nil {
		return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSProfileUnavailable
	}
	if !IsOpenAIOAuthOSProfileOwner(owner) {
		return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSProfileUnavailable
	}
	profiles := CloneOpenAIOAuthOSProfiles(owner.OpenAIOAuthOSProfiles)
	if !OpenAIOAuthOSProfilesComplete(profiles) {
		if repository, ok := repo.(OpenAIOAuthOSProfilesEnsurer); ok && owner.ID > 0 {
			profiles, err = repository.EnsureOpenAIOAuthOSProfiles(ctx, owner.ID)
		} else {
			return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSProfileUnavailable
		}
		if err != nil {
			return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSProfileUnavailable
		}
	}
	if !OpenAIOAuthOSProfilesComplete(profiles) {
		return OpenAIOAuthOSProfile{}, ErrOpenAIOAuthOSProfileUnavailable
	}
	if osFamily = NormalizeOpenAIOSFamily(osFamily); osFamily == "" {
		osFamily = profiles.DefaultOS
	}
	return profiles.Profiles[osFamily], nil
}
