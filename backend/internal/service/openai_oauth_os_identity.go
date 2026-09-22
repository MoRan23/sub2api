package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// One ingress request (or WebSocket connection) freezes an independent snapshot
// per credential owner. Failover may select a new owner, but returning to an
// already-used owner must not rotate its profile or daily roots mid-request.
const openAIOAuthOSSelectionsContextKey = "openai_oauth_os_selections"

type openAIOAuthOSSelection struct {
	CredentialOS            string
	AuthorizationGeneration string
	OwnerID                 int64
	DefaultOS               string
	Profile                 OpenAIOAuthOSProfile
	Profiles                *OpenAIOAuthOSProfiles
	Source                  string
	ReceivedAt              time.Time
	DailyEnabled            bool
	DailyBusinessDate       string
	DailyStreamRoot         string
	DailySyncRoot           string
}

type openAIOAuthOSSelectionContextKey struct{}

func captureOpenAIOAuthProfileRequest(c *gin.Context, body []byte) {
	if _, captured := OpenAIOAuthIdentityCaptureFromContext(c); captured {
		return
	}
	capture := OpenAIOAuthIdentityCapture{}
	capture.UserAgent, capture.OSFamily, capture.OSSource = captureOpenAIRequestOS(c, body)
	if c != nil {
		capture.UserAgentVersion = c.GetHeader("version")
	}
	SetOpenAIOAuthIdentityCapture(c, capture)
}

func openAIOAuthOSSelectionFromContext(ctx context.Context) (openAIOAuthOSSelection, bool) {
	if ctx == nil {
		return openAIOAuthOSSelection{}, false
	}
	selection, ok := ctx.Value(openAIOAuthOSSelectionContextKey{}).(openAIOAuthOSSelection)
	return selection, ok && selection.Profile.OSFamily != ""
}

func (s *OpenAIGatewayService) resolveOpenAIOAuthOSSelection(ctx context.Context, c *gin.Context, account *Account, capture OpenAIOAuthIdentityCapture) (openAIOAuthOSSelection, bool, error) {
	var repo AccountRepository
	if s != nil {
		repo = s.accountRepo
	}
	// The first identity for an owner is frozen for this request/connection.
	// A later account reload or default-OS change does not reselect its machine.
	if selection, exists := openAIOAuthOSSelectionForAccount(c, account); exists {
		return selection, true, nil
	}
	requestedOS := capture.OSFamily
	if frozen := OpenAIRequestOSFromContext(ctx); frozen.Captured {
		requestedOS = frozen.Family
	}
	credentialOS, authorizationGeneration := "", ""
	if RequiresOpenAIOAuthOSAuthorization(account) {
		resolved, err := ResolveOpenAIOAuthIdentityAccount(ctx, repo, account, requestedOS)
		if err != nil {
			return openAIOAuthOSSelection{}, false, err
		}
		account = resolved
		credentialOS, authorizationGeneration = resolved.OpenAIOAuthCredentialOS, resolved.OpenAIOAuthAuthorizationGeneration
		requestedOS = credentialOS
	}
	owner, err := resolveOpenAIInstallationIdentityAccount(ctx, repo, account)
	if err != nil {
		return openAIOAuthOSSelection{}, false, err
	}
	if !IsOpenAIOAuthOSProfileOwner(owner) {
		return openAIOAuthOSSelection{}, false, nil
	}
	if !OpenAIOAuthOSProfilesComplete(owner.OpenAIOAuthOSProfiles) {
		if _, supported := repo.(OpenAIOAuthOSProfilesEnsurer); !supported {
			// Legacy adapters without durable profile storage keep their existing
			// identity instead of minting a different machine on every request.
			return openAIOAuthOSSelection{}, false, nil
		}
	}
	profile, err := ResolveOpenAIOAuthOSProfile(ctx, repo, owner, requestedOS)
	if err != nil {
		return openAIOAuthOSSelection{}, false, err
	}
	// The persistent default describes the daily pool's compatibility root.
	// An empty profile request on a scoped account resolves its frozen slot,
	// which can differ from that persistent default.
	defaultOS := ""
	if owner.OpenAIOAuthOSProfiles != nil {
		defaultOS = NormalizeOpenAIOSFamily(owner.OpenAIOAuthOSProfiles.DefaultOS)
	}
	if defaultOS == "" {
		return openAIOAuthOSSelection{}, false, ErrOpenAIOAuthOSProfileUnavailable
	}
	receivedAt := capture.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	source := capture.OSSource
	if source == "" {
		source = "account_default"
	}
	selection := openAIOAuthOSSelection{OwnerID: owner.ID, DefaultOS: defaultOS, Profile: profile, Source: source, ReceivedAt: receivedAt,
		CredentialOS: credentialOS, AuthorizationGeneration: authorizationGeneration, Profiles: CloneOpenAIOAuthOSProfiles(owner.OpenAIOAuthOSProfiles)}
	if !capture.ReceivedAt.IsZero() && capture.RequestTurn.ID != "" && s != nil && s.oauthDailySessionRepo != nil && s.oauthDailySessionRotationEnabled(ctx) {
		if daily, ok := s.oauthDailySessionRepo.(OAuthDailySessionOSRepository); ok {
			pool, poolErr := daily.GetOrCreateOAuthDailySessionPoolForOS(ctx, owner.ID, selection.DefaultOS, receivedAt)
			if poolErr != nil {
				return selection, false, poolErr
			}
			roots, exists := pool.OSRoots[profile.OSFamily]
			if !exists {
				return selection, false, fmt.Errorf("missing OAuth daily roots for %s", profile.OSFamily)
			}
			streamRoot, streamErr := canonicalUUIDv7(roots.StreamSessionID)
			syncRoot, syncErr := canonicalUUIDv7(roots.SyncSessionID)
			if streamErr != nil || syncErr != nil {
				return selection, false, fmt.Errorf("invalid OAuth daily roots for %s", profile.OSFamily)
			}
			selection.DailyEnabled = true
			selection.DailyBusinessDate = pool.BusinessDate
			selection.DailyStreamRoot, selection.DailySyncRoot = streamRoot, syncRoot
		}
	}
	if c != nil {
		selections := make(map[int64]openAIOAuthOSSelection)
		if value, found := c.Get(openAIOAuthOSSelectionsContextKey); found {
			if existing, valid := value.(map[int64]openAIOAuthOSSelection); valid {
				for id, frozen := range existing {
					selections[id] = frozen
				}
			}
		}
		selections[owner.ID] = selection
		c.Set(openAIOAuthOSSelectionsContextKey, selections)
	}
	return selection, true, nil
}

func openAIOAuthOSSelectionForAccount(c *gin.Context, account *Account) (openAIOAuthOSSelection, bool) {
	if c == nil || account == nil {
		return openAIOAuthOSSelection{}, false
	}
	ownerID := account.ID
	if account.IsShadow() {
		ownerID = *account.ParentAccountID
	}
	value, found := c.Get(openAIOAuthOSSelectionsContextKey)
	if !found {
		return openAIOAuthOSSelection{}, false
	}
	selections, valid := value.(map[int64]openAIOAuthOSSelection)
	if !valid {
		return openAIOAuthOSSelection{}, false
	}
	selection, exists := selections[ownerID]
	return selection, exists
}

func bindOpenAIOAuthOSSelection(plan *OpenAIOAuthIdentityPlan, selection openAIOAuthOSSelection) {
	plan.CredentialOS, plan.AuthorizationGeneration = selection.CredentialOS, selection.AuthorizationGeneration
	plan.OSFamily, plan.OSSource = selection.Profile.OSFamily, selection.Source
	plan.ReceivedAt = selection.ReceivedAt
	plan.OSProfile = selection.Profile
	plan.OSDefault = selection.DefaultOS
	plan.OSOwnerID = selection.OwnerID
	plan.DailyRootsEnabled = selection.DailyEnabled
	plan.DailyBusinessDate = selection.DailyBusinessDate
	plan.DailyStreamRoot, plan.DailySyncRoot = selection.DailyStreamRoot, selection.DailySyncRoot
}

func openAIOAuthOSSelectionFromPlan(plan OpenAIOAuthIdentityPlan) openAIOAuthOSSelection {
	return openAIOAuthOSSelection{OwnerID: plan.OSOwnerID, DefaultOS: plan.OSDefault, Profile: plan.OSProfile, Source: plan.OSSource, ReceivedAt: plan.ReceivedAt,
		CredentialOS: plan.CredentialOS, AuthorizationGeneration: plan.AuthorizationGeneration,
		DailyEnabled: plan.DailyRootsEnabled, DailyBusinessDate: plan.DailyBusinessDate, DailyStreamRoot: plan.DailyStreamRoot, DailySyncRoot: plan.DailySyncRoot}
}

func openAIOAuthOSInstallationResolution(account *Account, selection openAIOAuthOSSelection, clientID string) installationIDResolution {
	result := installationIDResolution{ClientID: strings.TrimSpace(clientID)}
	if account != nil && account.IsOpenAIInstallationPinEnabled() {
		result.Enabled = true
		result.OutboundID = selection.Profile.InstallationID
	}
	return result
}
