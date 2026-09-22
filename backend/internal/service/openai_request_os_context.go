package service

import (
	"context"
	"maps"

	"github.com/gin-gonic/gin"
)

// OpenAIRequestOS is immutable ingress evidence, independent of the candidate
// account. Captured distinguishes an unknown OS from evidence not read yet.
type OpenAIRequestOS struct {
	Family   string `json:"family"`
	Source   string `json:"source"`
	Captured bool   `json:"captured"`
}

type openAIRequestOSContextKey struct{}

func OpenAIRequestOSFromContext(ctx context.Context) OpenAIRequestOS {
	if ctx == nil {
		return OpenAIRequestOS{}
	}
	value, _ := ctx.Value(openAIRequestOSContextKey{}).(OpenAIRequestOS)
	return value
}

// ContextWithOpenAIRequestOS keeps the first captured value, including unknown.
// Unknown remains empty until a credential owner supplies its own default OS.
func ContextWithOpenAIRequestOS(ctx context.Context, value OpenAIRequestOS) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if OpenAIRequestOSFromContext(ctx).Captured {
		return ctx
	}
	value.Family = NormalizeOpenAIOSFamily(value.Family)
	value.Captured = true
	return context.WithValue(ctx, openAIRequestOSContextKey{}, value)
}

// CaptureOpenAIRequestOS must run against untouched ingress data before account
// selection. Subsequent compatibility rewrites and WS frames cannot recapture it.
func CaptureOpenAIRequestOS(c *gin.Context, body []byte) OpenAIRequestOS {
	if c != nil && c.Request != nil {
		if frozen := OpenAIRequestOSFromContext(c.Request.Context()); frozen.Captured {
			return frozen
		}
	}
	_, family, source := detectOpenAIRequestOS(c, body)
	value := OpenAIRequestOS{Family: family, Source: source, Captured: true}
	if c != nil && c.Request != nil {
		c.Request = c.Request.WithContext(ContextWithOpenAIRequestOS(c.Request.Context(), value))
	}
	return value
}

// Keep service and Gin contexts on the same first capture, including unknown,
// before identity capture can inspect another body or WS frame.
func captureOpenAIRequestOSContext(ctx context.Context, c *gin.Context, body []byte) context.Context {
	if frozen := OpenAIRequestOSFromContext(ctx); frozen.Captured && c != nil && c.Request != nil {
		c.Request = c.Request.WithContext(ContextWithOpenAIRequestOS(c.Request.Context(), frozen))
	}
	return ContextWithOpenAIRequestOS(ctx, CaptureOpenAIRequestOS(c, body))
}

// Capture before transformations and select the identity only after scheduling.
// The identity projection leaves the selected account's OAuth credentials intact.
func (s *OpenAIGatewayService) prepareOpenAIOAuthRequestScope(ctx context.Context, c *gin.Context, account *Account, body []byte) (context.Context, *Account, error) {
	ctx = captureOpenAIRequestOSContext(ctx, c, body)
	if !RequiresOpenAIOAuthOSAuthorization(account) {
		return ctx, account, nil
	}
	if selection, exists := openAIOAuthOSSelectionForAccount(c, account); exists {
		out := *account
		out.Credentials = maps.Clone(account.Credentials)
		if out.Credentials == nil {
			out.Credentials = make(map[string]any)
		}
		out.Extra = maps.Clone(account.Extra)
		if out.Extra == nil {
			out.Extra = make(map[string]any)
		}
		out.Credentials["user_agent"] = selection.Profile.UserAgent
		out.Extra[openAIPinnedInstallationIDKey] = selection.Profile.InstallationID
		out.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(selection.Profiles)
		out.OpenAIOAuthCredentialOS, out.OpenAIOAuthCredentialOwnerID = selection.Profile.OSFamily, selection.OwnerID
		return ctx, &out, nil
	}
	if account.OpenAIOAuthCredentialOS != "" {
		return ctx, account, nil
	}
	scoped, err := ResolveOpenAIOAuthIdentityAccount(ctx, s.accountRepo, account, OpenAIRequestOSFromContext(ctx).Family)
	return ctx, scoped, err
}
