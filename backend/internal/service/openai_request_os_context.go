package service

import (
	"context"

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

// Direct service callers can enter without a handler-selected credential scope.
// Capture before transformations, propagate it to the local context too, and
// project a private request-owned account before obtaining any bearer token.
func (s *OpenAIGatewayService) prepareOpenAIOAuthRequestScope(ctx context.Context, c *gin.Context, account *Account, body []byte) (context.Context, *Account, error) {
	ctx = captureOpenAIRequestOSContext(ctx, c, body)
	if !RequiresOpenAIOAuthOSAuthorization(account) || account.OpenAIOAuthCredentialOS != "" {
		return ctx, account, nil
	}
	scoped, err := ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account, OpenAIRequestOSFromContext(ctx).Family)
	return ctx, scoped, err
}

// openAIAccountOSAuthorizationEligible evaluates credential-owner eligibility.
// Shadow business accounts use their parent's shared grant. Request OS does not
// participate in authorization eligibility.
func openAIAccountOSAuthorizationEligible(ctx context.Context, account *Account, lookup func(int64) *Account) bool {
	if !RequiresOpenAIOAuthOSAuthorization(account) {
		return true
	}
	owner := account
	if account.IsShadow() {
		if lookup == nil || account.ParentAccountID == nil {
			return false
		}
		owner = lookup(*account.ParentAccountID)
		if owner == nil {
			return false
		}
	}
	return OpenAIOAuthOSAuthorizationAvailable(owner, "")
}

func openAIParentHealthyForShadow(ctx context.Context, account *Account, lookup func(int64) *Account) bool {
	return parentHealthyForShadow(account, lookup) && openAIAccountOSAuthorizationEligible(ctx, account, lookup)
}
