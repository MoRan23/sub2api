package service

import (
	"context"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// FingerprintServiceTierPolicy records only bounded policy decisions. The final
// tier comes from the physical outbound body, after the global policy and wire
// finalizers have run; requested and forced tiers describe the earlier decision.
type FingerprintServiceTierPolicy struct {
	Source        string `json:"source"`
	RequestedTier string `json:"requested_tier,omitempty"`
	ForcedTier    string `json:"forced_tier,omitempty"`
	EffectiveTier string `json:"effective_tier,omitempty"`
	Capability    string `json:"capability"`
}

const openAIServiceTierPolicyCaptureKey = "openai_service_tier_policy_capture"

type openAIServiceTierPolicyContextKey struct{}

type openAIServiceTierPolicyCapture struct {
	mu        sync.Mutex
	accountID int64
	value     FingerprintServiceTierPolicy
	set       bool
}

func withOpenAIServiceTierPolicyContext(ctx context.Context, c *gin.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || globalFingerprintObserver == nil || !globalFingerprintObserver.enabled.Load() {
		return ctx
	}
	if existing, _ := ctx.Value(openAIServiceTierPolicyContextKey{}).(*openAIServiceTierPolicyCapture); existing != nil {
		return ctx
	}
	var capture *openAIServiceTierPolicyCapture
	if value, exists := c.Get(openAIServiceTierPolicyCaptureKey); exists {
		capture, _ = value.(*openAIServiceTierPolicyCapture)
	}
	if capture == nil {
		capture = &openAIServiceTierPolicyCapture{}
		c.Set(openAIServiceTierPolicyCaptureKey, capture)
	}
	if existing, _ := ctx.Value(openAIServiceTierPolicyContextKey{}).(*openAIServiceTierPolicyCapture); existing == capture {
		return ctx
	}
	return context.WithValue(ctx, openAIServiceTierPolicyContextKey{}, capture)
}

// WS frames own their decision independently of the long-lived Gin context.
// Queueing a later frame must not replace an earlier physical attempt's policy.
func withOpenAIServiceTierPolicyFrameContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if globalFingerprintObserver == nil || !globalFingerprintObserver.enabled.Load() {
		return ctx
	}
	return context.WithValue(ctx, openAIServiceTierPolicyContextKey{}, &openAIServiceTierPolicyCapture{})
}

func snapshotOpenAIServiceTierPolicy(ctx context.Context, account *Account) *FingerprintServiceTierPolicy {
	if ctx == nil || account == nil {
		return nil
	}
	capture, _ := ctx.Value(openAIServiceTierPolicyContextKey{}).(*openAIServiceTierPolicyCapture)
	return capture.snapshot(account.ID)
}

func withOpenAIServiceTierPolicySnapshot(ctx context.Context, account *Account, observation *FingerprintServiceTierPolicy) context.Context {
	ctx = withOpenAIServiceTierPolicyFrameContext(ctx)
	if account != nil && observation != nil {
		recordOpenAIServiceTierPolicy(ctx, account, observation.Source, observation.RequestedTier, observation.ForcedTier, observation.Capability)
	}
	return ctx
}

func recordOpenAIServiceTierPolicy(ctx context.Context, account *Account, source, requested, forced, capability string) {
	if ctx == nil || account == nil {
		return
	}
	capture, _ := ctx.Value(openAIServiceTierPolicyContextKey{}).(*openAIServiceTierPolicyCapture)
	if capture == nil {
		return
	}
	observation := FingerprintServiceTierPolicy{
		RequestedTier: fingerprintServiceTier(requested),
		ForcedTier:    fingerprintServiceTier(forced),
	}
	switch source {
	case "account", "group", "none":
		observation.Source = strings.Clone(source)
	default:
		observation.Source = "none"
	}
	switch capability {
	case "supported", "unsupported", "unavailable", "not_required", "guardian":
		observation.Capability = strings.Clone(capability)
	default:
		observation.Capability = "unavailable"
	}
	capture.mu.Lock()
	capture.accountID, capture.value, capture.set = account.ID, observation, true
	capture.mu.Unlock()
}

func fingerprintServiceTier(tier string) string {
	// Do not retain arbitrary client values, or a substring of a large body.
	if len(tier) > 32 {
		return ""
	}
	return strings.Clone(normalizedOpenAIServiceTierValue(tier))
}

func fingerprintServiceTierPolicyFromContext(c *gin.Context, account *Account, hasBody bool) *FingerprintServiceTierPolicy {
	if c == nil || account == nil || !hasBody {
		return nil // A physical handshake does not carry a request service tier.
	}
	value, exists := c.Get(openAIServiceTierPolicyCaptureKey)
	if !exists {
		return nil
	}
	capture, _ := value.(*openAIServiceTierPolicyCapture)
	return capture.snapshot(account.ID)
}

func (capture *openAIServiceTierPolicyCapture) snapshot(accountID int64) *FingerprintServiceTierPolicy {
	if capture == nil {
		return nil
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if !capture.set || capture.accountID != accountID {
		return nil
	}
	observation := capture.value
	return &observation
}

func populateFingerprintServiceTierPolicy(observation *FingerprintServiceTierPolicy, tier gjson.Result) {
	if observation == nil {
		return
	}
	observation.EffectiveTier = ""
	if tier.Type == gjson.String {
		observation.EffectiveTier = fingerprintServiceTier(tier.Str)
	}
}
